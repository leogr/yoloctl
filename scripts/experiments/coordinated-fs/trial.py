#!/usr/bin/env python3
"""Two isolated synthetic Linux guests; never exports real accounts or workdirs.

Needs the reviewed build.py output, /dev/kvm, bubblewrap and enabled unprivileged
user namespaces. There is no fallback to an unconfined VMM. Artifacts are kept
in a fresh /var/tmp/ycf-trial-* directory. Run this on the reference host.
"""
import argparse
import contextlib
import datetime
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import struct
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent
CONTRACT = HERE.parent / "storage-contract.py"


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def copy_file(source, root, destination=None):
    destination = destination or source
    target = root / str(destination).lstrip("/")
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    shutil.copymode(source, target)


def elf_interpreter(source):
    """Read PT_INTERP without executing the binary; this trial is ELF64/LE only."""
    with source.open("rb") as stream:
        header = stream.read(64)
        if len(header) != 64 or header[:7] != b"\x7fELF\x02\x01\x01":
            raise ValueError("expected a little-endian ELF64 file: " + str(source))
        offset = struct.unpack_from("<Q", header, 32)[0]
        entry_size, count = struct.unpack_from("<HH", header, 54)
        if entry_size != 56 or count > 512:
            raise ValueError("unsupported ELF program headers: " + str(source))
        for index in range(count):
            stream.seek(offset + index * entry_size)
            entry = stream.read(entry_size)
            if len(entry) != entry_size:
                raise ValueError("truncated ELF program header")
            if struct.unpack_from("<I", entry)[0] != 3:  # PT_INTERP
                continue
            position = struct.unpack_from("<Q", entry, 8)[0]
            length = struct.unpack_from("<Q", entry, 32)[0]
            if not 2 <= length <= 4096:
                raise ValueError("invalid ELF interpreter length")
            stream.seek(position)
            raw = stream.read(length)
            if len(raw) != length or raw[-1:] != b"\0" or b"\0" in raw[:-1]:
                raise ValueError("invalid ELF interpreter path")
            interpreter = Path(os.fsdecode(raw[:-1]))
            if not interpreter.is_absolute() or ".." in interpreter.parts:
                raise ValueError("ELF interpreter must be an absolute path")
            return interpreter
    return None


def dependencies(source, root):
    # ldd may resolve /lib64/ld-linux-*.so to /usr/lib64/ld-linux-*.so.
    # execve still opens the exact PT_INTERP path, and our minimal root does
    # not inherit the host's merged-/usr symlinks.
    interpreter = elf_interpreter(source)
    if interpreter is not None:
        copy_file(interpreter, root)
    result = subprocess.run(["ldd", str(source)], capture_output=True, text=True, timeout=10)
    if result.returncode and "not a dynamic executable" not in result.stdout + result.stderr:
        raise RuntimeError("ldd failed for " + str(source))
    if "not found" in result.stdout:
        raise RuntimeError("missing dependency for " + str(source))
    for line in result.stdout.splitlines():
        match = re.search(r"(?:=>\s*)?(/\S+)\s+\(", line)
        if match:
            path = Path(match[1])
            # Trial-built libraries have their own stable location in the
            # minimal VMM root; system dependencies retain their loader paths.
            if path.name.startswith("libkrun.so"):
                continue
            copy_file(path, root)


def runtime(build, trial, refresh_launcher=False):
    root = trial / "runtime"
    root.mkdir()
    for directory in ("proc", "dev", "tmp", "control", "account", "guest", "kernel", "lib", "bin"):
        (root / directory).mkdir()
    copy_file(build / "artifacts/lib/libkrun.so", root, "/lib/libkrun.so.1")
    copy_file(build / "artifacts/kernel/vmlinux", root, "/kernel/vmlinux")
    if refresh_launcher:
        # Recompile this small harness only. The original build and its
        # service/kernel/launcher binaries remain untouched and hash-checked.
        command = ["cc", "-O2", "-Wall", "-Wextra", "-Werror", "-isystem", str(build / "libkrun/include"),
                   str(HERE / "vmm.c"), "-L", str(build / "artifacts/lib"), "-lkrun",
                   "-Wl,-rpath,$ORIGIN/../lib", "-o", str(root / "bin/ycf-vmm")]
        result = subprocess.run(command, capture_output=True, text=True, timeout=30)
        (trial / "launcher-build.log").write_text(result.stdout + result.stderr)
        if result.returncode:
            raise RuntimeError("launcher rebuild failed; inspect launcher-build.log")
    else:
        copy_file(build / "artifacts/bin/ycf-vmm", root, "/bin/ycf-vmm")
    dependencies(root / "bin/ycf-vmm", root)
    dependencies(build / "artifacts/lib/libkrun.so", root)
    return {"refreshed": refresh_launcher, "sha256": digest(root / "bin/ycf-vmm"),
            "source_sha256": digest(HERE / "vmm.c"),
            "header_sha256": digest(build / "libkrun/include/libkrun.h")}


def guest_template(trial):
    root = trial / "guest-template"
    root.mkdir()
    for directory in ("dev/pts", "dev/shm", "proc", "sys/fs/cgroup", "tmp", "account", "probe", "sfs"):
        (root / directory).mkdir(parents=True, exist_ok=True)
    interpreter = Path("/usr/bin/python3")
    info = json.loads(subprocess.check_output([str(interpreter), "-c",
        "import json,sysconfig; print(json.dumps({'stdlib':sysconfig.get_path('stdlib')}))"], text=True))
    stdlib = Path(info["stdlib"])
    shutil.copytree(stdlib, root / str(stdlib).lstrip("/"),
                    ignore=shutil.ignore_patterns("site-packages", "dist-packages", "__pycache__",
                                                 "test", "tests", "idlelib", "tkinter", "ensurepip"))
    copy_file(interpreter, root, "/usr/bin/python3")
    dependencies(interpreter, root)
    for extension in stdlib.rglob("*.so"):
        relative = extension.relative_to(stdlib)
        if any(part in {"site-packages", "dist-packages", "test", "tests"} for part in relative.parts):
            continue
        dependencies(extension, root)
    copy_file(CONTRACT, root, "/probe/storage-contract.py")
    copy_file(HERE / "guest.py", root, "/probe/guest.py")
    return root


def preflight(trial):
    checks = {}
    for name, root, command, expected_code, marker in (
            ("vmm", trial / "runtime", ["/bin/ycf-vmm"], 2,
             "Expected one synthetic storage-contract run ID"),
            ("python", trial / "guest-template", ["/usr/bin/python3", "-c",
             "import ctypes, mmap, sqlite3; print('YCF_PYTHON_READY')"], 0, "YCF_PYTHON_READY")):
        argv = ["bwrap", "--unshare-all", "--die-with-parent", "--ro-bind", str(root), "/",
                "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--chdir", "/",
                "--clearenv", "--setenv", "LD_LIBRARY_PATH", "/lib",
                "--setenv", "PYTHONDONTWRITEBYTECODE", "1", "--", *command]
        result = subprocess.run(argv, capture_output=True, text=True, stdin=subprocess.DEVNULL, timeout=15)
        output = result.stdout + result.stderr
        (trial / ("preflight-" + name + ".log")).write_text(output)
        if result.returncode != expected_code or marker not in output:
            raise RuntimeError(name + " packaging preflight failed; inspect preflight-" + name + ".log")
        checks[name] = "passed in isolated namespace without KVM or account data"
    return checks


def sandbox_command(trial, name, run_id):
    return ["bwrap", "--unshare-all", "--die-with-parent", "--ro-bind", str(trial / "runtime"), "/",
            "--bind", str(trial / "control"), "/control", "--bind", str(trial / "account"), "/account",
            "--bind", str(trial / ("guest-" + name)), "/guest", "--proc", "/proc", "--dev", "/dev",
            "--dev-bind", "/dev/kvm", "/dev/kvm", "--tmpfs", "/tmp", "--chdir", "/", "--clearenv",
            "--setenv", "PATH", "/bin", "--setenv", "LD_LIBRARY_PATH", "/lib",
            "--setenv", "YCF_ROOT", "/account", "--setenv", "YCF_CONTROL", "/control",
            "--setenv", "YCF_CLIENT", "1" if name == "a" else "2", "--", "/bin/ycf-vmm", run_id]


def endpoint(arguments):
    # Called only by the contract's Endpoint adapter. Keep its standard code
    # transport unchanged but run the identical file inside the guest, avoiding
    # an oversized kernel command line.
    if len(arguments) != 9:
        raise ValueError("invalid endpoint invocation")
    trial = Path(arguments[0]).resolve(strict=True)
    name, executable, unbuffered, flag, code, worker, account, run_id = arguments[1:]
    if (name not in {"a", "b"} or executable != "/usr/bin/python3" or unbuffered != "-u"
            or flag != "-c" or worker != "--worker" or account != "/account"
            or not re.fullmatch(r"storage-contract-[0-9a-f]{32}", run_id)
            or code != CONTRACT.read_text()):
        raise ValueError("endpoint arguments differ from the reviewed synthetic probe")
    if (trial / "marker").read_text() != "ycf-synthetic-trial-v1\n":
        raise ValueError("missing synthetic trial marker")
    log = os.open(trial / ("guest-" + name + ".log"), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    os.dup2(log, 2)
    os.close(log)
    # Endpoint already starts a fresh process group. Do not create another
    # session here; its timeout cleanup must reach bubblewrap and the VMM.
    command = sandbox_command(trial, name, run_id)
    # Keep every output byte available for diagnosis, including non-protocol
    # boot messages that the contract deliberately ignores. Remain in the
    # Endpoint process group so timeout cleanup still kills/reaps the VMM.
    process = subprocess.Popen(command, stdout=subprocess.PIPE)
    with (trial / ("guest-" + name + "-stdout.log")).open("xb") as output:
        retained = 0
        while chunk := process.stdout.read1(65536):
            if retained < 4 * 1024 * 1024:
                saved = chunk[:4 * 1024 * 1024 - retained]
                output.write(saved)
                output.flush()
                retained += len(saved)
            sys.stdout.buffer.write(chunk)
            sys.stdout.buffer.flush()
    process.stdout.close()
    return process.wait()


def main():
    if len(sys.argv) > 1 and sys.argv[1] == "--endpoint":
        return endpoint(sys.argv[2:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--build", type=Path, required=True)
    parser.add_argument("--refresh-launcher", action="store_true",
                        help="compile current vmm.c into this trial; reuse the verified kernel/service")
    parser.add_argument("--control", choices=("mapped-resize", "coordinator-loss", "stopped-peer", "large-append"),
                        help="run one additional bounded control instead of the original contract")
    parser.add_argument("--output-parent", type=Path, default=Path("/var/tmp"),
                        help="parent for a new disposable trial directory")
    args = parser.parse_args()
    for tool in ("bwrap", "ldd"):
        if not shutil.which(tool):
            parser.error(f"requires {tool}; no unconfined fallback")
    if args.refresh_launcher and not shutil.which("cc"):
        parser.error("--refresh-launcher requires cc")
    if not os.access("/dev/kvm", os.R_OK | os.W_OK):
        parser.error("requires accessible /dev/kvm on the reference host")
    build = args.build.resolve(strict=True)
    build_report = json.loads((build / "build-report.json").read_text())
    if build_report["status"] != "built":
        parser.error("build did not complete")
    for name, expected in build_report["artifacts"].items():
        if digest(build / name) != expected:
            parser.error("build artifact hash changed: " + name)
    # A corrected packaging/runner script can reuse these exact compiled
    # artifacts. Retain the original report and record the current runner
    # separately, without weakening the kernel/service/guest input checks.
    for name in ("guest.py", "coordinator.py", "libkrun-coordinator.rs",
                 "kernel.patch", "libkrun.patch", "pins.json"):
        if digest(HERE / name) != build_report["experiment_sources"].get(name):
            parser.error("experiment changed since build: " + name)
    if not args.refresh_launcher and digest(HERE / "vmm.c") != build_report["experiment_sources"]["vmm.c"]:
        parser.error("launcher changed; use --refresh-launcher to compile it without rebuilding the kernel/service")
    trial = Path(tempfile.mkdtemp(prefix="ycf-trial-", dir=args.output_parent.resolve(strict=True)))
    print("Trial artifacts: " + str(trial), flush=True)
    (trial / "marker").write_text("ycf-synthetic-trial-v1\n")
    for name in ("account", "control"):
        (trial / name).mkdir(mode=0o700)
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "status": "failed", "host_kernel": os.uname().release, "build": build_report,
              "runtime_sources": {str(path.relative_to(HERE.parent)): digest(path)
                                  for path in (HERE / "trial.py", HERE / "guest.py",
                                               HERE / "coordinator.py", HERE / "vmm.c", CONTRACT)},
              "limits": "Synthetic correctness trial only: incomplete locks/EOF/operations, no durability, agent, security, workdir, performance or macOS qualification."}
    if args.control:
        report["control"] = args.control
        report["runtime_sources"]["coordinated-fs/followup.py"] = digest(HERE / "followup.py")
    coordinator = None
    try:
        spec = importlib.util.spec_from_file_location("contract", CONTRACT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        report["host_backing_filesystem"] = module.filesystem(str(trial))
        report["host_page_size"] = os.sysconf("SC_PAGESIZE")
        if report["host_backing_filesystem"] == "9p":
            raise RuntimeError("9p backing refused; this is a reference-host trial")
        report["launcher"] = runtime(build, trial, args.refresh_launcher)
        template = guest_template(trial)
        report["packaging_preflight"] = preflight(trial)
        for name in ("a", "b"):
            shutil.copytree(template, trial / ("guest-" + name))
        with (trial / "coordinator.log").open("xb") as log:
            coordinator = subprocess.Popen([sys.executable, str(HERE / "coordinator.py"), str(trial / "control"),
                                            "--parent-pid", str(os.getpid())],
                                           stdout=log, stderr=log, start_new_session=True)
        deadline = time.monotonic() + 5
        while not (trial / "control/control.sock").exists():
            if coordinator.poll() is not None or time.monotonic() >= deadline:
                raise RuntimeError("coordinator did not start")
            time.sleep(0.01)
        endpoints = [{"name": name, "root": "/account", "python": "/usr/bin/python3",
                      "prefix": [sys.executable, str(HERE / "trial.py"), "--endpoint", str(trial), name]}
                     for name in ("a", "b")]
        (trial / "endpoints.json").write_text(json.dumps(endpoints))
        # Own the Endpoint children directly so the contract's finally block
        # can terminate and reap both bubblewrap process groups on every exit.
        def deadline(_signum, _frame):
            raise TimeoutError("global synthetic trial deadline exceeded")

        def interrupted(_signum, _frame):
            raise KeyboardInterrupt("synthetic trial interrupted")

        previous_alarm = signal.signal(signal.SIGALRM, deadline)
        previous_term = signal.signal(signal.SIGTERM, interrupted)
        previous_argv = sys.argv
        try:
            sys.argv = [str(CONTRACT), "--endpoints", str(trial / "endpoints.json"),
                        "--report", str(trial / "contract.json"), "--keep"]
            signal.alarm(180)
            with (trial / "probe.log").open("x") as log, contextlib.redirect_stdout(log):
                if args.control:
                    control_spec = importlib.util.spec_from_file_location("followup", HERE / "followup.py")
                    control_module = importlib.util.module_from_spec(control_spec)
                    control_spec.loader.exec_module(control_module)
                    result = control_module.run(module, endpoints, trial, coordinator, args.control)
                else:
                    result = module.main()
        finally:
            signal.alarm(0)
            signal.signal(signal.SIGALRM, previous_alarm)
            signal.signal(signal.SIGTERM, previous_term)
            sys.argv = previous_argv
        if result:
            raise RuntimeError("control failed; inspect control-report.json and guest logs" if args.control
                               else "contract failed; inspect contract.json and guest logs")
        contract = json.loads((trial / ("control-report.json" if args.control else "contract.json")).read_text())
        report["followup" if args.control else "contract"] = contract
        if len({info["boot_id"] for info in contract["endpoints"].values()}) != 2:
            raise RuntimeError("distinct Linux guest kernels not established")
        report["dax_mapping_messages"] = {}
        for name in ("a", "b"):
            log = (trial / ("guest-" + name + ".log")).read_text()
            count = log.count("YCF_SETUPMAPPING")
            if not count or "YCF_DAX " not in log:
                raise RuntimeError("DAX setup and actual file DAX flags were not both observed")
            report["dax_mapping_messages"][name] = count
        report["status"] = "completed-bounded-followup" if args.control else "passed-bounded-contract"
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        # Coordinator death closes both reverse channels. VMMs abort and
        # bubblewrap --die-with-parent also fences them if an adapter is lost.
        if coordinator and coordinator.poll() is None:
            os.killpg(coordinator.pid, signal.SIGKILL)
            coordinator.wait(timeout=5)
        (trial / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"status": report["status"], "artifacts": str(trial), "error": report.get("error")}))
    return 0 if report["status"] in {"passed-bounded-contract", "completed-bounded-followup"} else 1


if __name__ == "__main__":
    sys.exit(main())
