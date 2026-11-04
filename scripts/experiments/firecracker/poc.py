#!/usr/bin/env python3
"""Isolated Linux/x86_64 Firecracker startup experiment; never invokes limactl.

Only files under an exclusively allocated build/trial directory are mutated.
The host opens /dev/kvm, but mounts no filesystems and configures no networking.
"""

import argparse
import concurrent.futures
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import statistics
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
import uuid

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
# Production and experimental images share one pinned kernel/input recipe.
import importlib.util
_inputs_spec = importlib.util.spec_from_file_location("yfc_inputs", REPO / "assets/vm/firecracker/inputs.py")
_inputs = importlib.util.module_from_spec(_inputs_spec)
_inputs_spec.loader.exec_module(_inputs)
PINS = _inputs.PINS
KERNEL_INPUTS, KERNEL_EXTRAS, KERNEL_DISABLED = _inputs.KERNEL_INPUTS, _inputs.KERNEL_EXTRAS, _inputs.KERNEL_DISABLED
digest, save_json, run = _inputs.digest, _inputs.save_json, _inputs.run
new_directory, download, tar_file = _inputs.new_directory, _inputs.download, _inputs.tar_file
require_platform, require_tools = _inputs.require_platform, _inputs.require_tools
publish_kernel, kernel = _inputs.publish_kernel, _inputs.kernel
DATA_SIZE = 256 * 1024 * 1024
PORT = 5252


def prepare(args):
    require_platform()
    require_tools(["go", "mkfs.ext4", "debugfs", "dpkg-deb", args.unsquashfs])
    runtime_pins = (REPO / "assets/embed.go").read_text()
    match = re.search(r'SandboxSHAAmd64\s*=\s*"([a-f0-9]{64})"', runtime_pins)
    if not match or match[1] != PINS["openshell.tgz"]["sha256"]:
        raise RuntimeError("PoC OpenShell pin differs from the production runtime; review pins and security gate")
    kernel_build = Path(args.kernel_build).resolve()
    kernel_manifest = json.loads((kernel_build / "kernel.json").read_text())
    if (kernel_manifest.get("inputs") != {name: PINS[name] for name in sorted(KERNEL_INPUTS)}
            or kernel_manifest.get("disabled") != KERNEL_DISABLED):
        raise RuntimeError("old/different kernel recipe; rebuild the current pinned kernel")
    if (kernel_manifest["kernel_sha256"] != digest(kernel_build / "kernel")
            or kernel_manifest["config_sha256"] != digest(kernel_build / "kernel.config")):
        raise RuntimeError("custom kernel artifacts changed")
    out = new_directory(args.output)
    print(f"Build artifacts: {out}", flush=True)
    inputs = out / "inputs"
    inputs.mkdir()
    cache = Path(args.cache).resolve() if args.cache else None
    image_inputs = set(PINS) - KERNEL_INPUTS
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        list(executor.map(lambda name: download(name, inputs, cache), sorted(image_inputs)))
    config = (kernel_build / "kernel.config").read_text()
    for option in KERNEL_DISABLED:
        if f"# CONFIG_{option} is not set" not in config.splitlines():
            raise RuntimeError(f"unsupported optional kernel module enabled: {option}")
    for option in ["CONFIG_EXT4_FS=y", "CONFIG_VIRTIO_BLK=y", "CONFIG_VIRTIO_MMIO=y",
                   "CONFIG_VIRTIO_VSOCKETS=y", "CONFIG_CGROUPS=y", "CONFIG_DEVTMPFS=y",
                   "CONFIG_SECURITY_LANDLOCK=y", "CONFIG_SECURITY=y", "CONFIG_SECCOMP=y",
                   "CONFIG_SECCOMP_FILTER=y", "CONFIG_NET_NS=y", "CONFIG_VETH=y", "CONFIG_NF_TABLES=y",
                   *[f"CONFIG_{name}=y" for name in KERNEL_EXTRAS]]:
        if option not in config.splitlines():
            raise RuntimeError(f"guest kernel lacks {option}")
    if not re.search(r'^CONFIG_LSM="(?:[^"\n]*,)?landlock(?:,|\")', config, re.M):
        raise RuntimeError("Landlock missing from the kernel's enabled LSM list")
    tar_file(inputs / "firecracker.tgz",
             "release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64", out / "firecracker")
    run([out / "firecracker", "--version"])
    stage = out / "rootfs-tree"
    with (out / "extract.log").open("x") as log:
        # Exclude device nodes; devtmpfs is mounted by guest PID 1.
        run([args.unsquashfs, "-no-xattrs", "-no-progress", "-processors", "2", "-excludes",
             "-dest", stage, inputs / "ubuntu.squashfs", "dev"], stdout=log, stderr=log)
    for required in ["usr/bin/python3", "usr/bin/findmnt", "usr/bin/mount", "usr/bin/mountpoint", "usr/bin/nsenter", "bin/sh"]:
        if not (stage / required).is_file():
            raise RuntimeError(f"incomplete rootfs extraction: missing {required}")
    for directory in ["dev", "proc", "sys", "run", "tmp", "mnt", "var", "poc",
                      "opt/yoloctl", "etc/yoloctl", "etc/openshell-tls", "poc-workdir", "usr/local/bin"]:
        (stage / directory).mkdir(exist_ok=True, parents=True)
    # No test-image SSH identities or credentials are needed by this guest.
    for name in ["root/.ssh", "etc/ssh", "var/lib/cloud"]:
        path = stage / name
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
    for name in ["containerd", "containerd-shim-runc-v2", "ctr", "nerdctl", "runc"]:
        tar_file(inputs / "nerdctl.tgz", f"bin/{name}", stage / "usr/local/bin" / name)
    tar_file(inputs / "openshell.tgz", "openshell-sandbox", stage / "usr/local/bin/openshell-sandbox")
    for name in sorted(image_inputs):
        if name.endswith(".deb"):
            # Extract into the private guest tree; never install packages on the host.
            run(["dpkg-deb", "-x", inputs / name, stage])
    # This fixture identity exists only in the new guest rootfs.
    with (stage / "etc/passwd").open("a") as stream:
        stream.write("poc-sandbox:x:2000:2000:PoC sandbox:/poc-workdir:/bin/bash\n")
    with (stage / "etc/group").open("a") as stream:
        stream.write("poc-sandbox:x:2000:\n")
    shutil.copyfile(REPO / "assets/sandbox/sandbox-policy.rego", stage / "poc/policy.rego")
    shutil.copyfile(HERE / "security-policy.yaml", stage / "poc/security-policy.yaml")
    shutil.copyfile(HERE / "security-probe.py", stage / "poc/security-probe.py")
    bundle = "firecracker-poc-" + uuid.uuid4().hex
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOMAXPROCS="2")
    for package, target in [("./cmd/yoloctl", "usr/local/bin/yoloctl"),
                            ("./scripts/experiments/firecracker/guest", "poc/init")]:
        run(["go", "build", "-p", "2", "-trimpath", "-ldflags",
             f"-s -w -X github.com/leogr/yoloctl/internal/controller.BuildID={bundle}",
             "-o", stage / target, package], cwd=REPO, env=env)
    (stage / "poc/containerd.toml").write_text(
        'version = 2\nroot = "/var/lib/containerd"\nstate = "/run/containerd"\n'
        'disabled_plugins = ["io.containerd.grpc.v1.cri", "io.containerd.cri.v1.runtime", '
        '"io.containerd.cri.v1.images"]\n')
    rootfs = out / "rootfs.ext4"
    with rootfs.open("xb") as stream:
        stream.truncate(1536 * 1024 * 1024)
    with (out / "filesystem.log").open("x") as log:
        run(["mkfs.ext4", "-q", "-F", "-O", "^orphan_file", "-E",
             "lazy_itable_init=0,lazy_journal_init=0,root_owner=0:0", "-d", stage, rootfs],
            stdout=log, stderr=log)
        # Rootless extraction uses host ownership. Normalize the actual guest
        # inodes, including symlinks, without chowning any host files or mounting.
        commands = out / "root-ownership.debugfs"
        with commands.open("x") as stream:
            for parent, dirs, files in os.walk(stage, followlinks=False):
                for name in dirs + files:
                    rel = "/" + str((Path(parent) / name).relative_to(stage))
                    if any(c in rel for c in '\n\r"'):
                        raise RuntimeError("unsupported image pathname for debugfs")
                    stream.write(f'set_inode_field "{rel}" uid 0\nset_inode_field "{rel}" gid 0\n')
        run(["debugfs", "-w", "-f", commands, rootfs], stdout=log, stderr=log)
    # Verify the critical trust paths in the built filesystem, not the staging tree.
    for path in ["/etc", "/etc/yoloctl", "/opt", "/opt/yoloctl", "/poc/init"]:
        result = run(["debugfs", "-R", f"stat {path}", rootfs], capture_output=True, text=True)
        if not re.search(r"User:\s+0\s+Group:\s+0\b", result.stdout):
            raise RuntimeError(f"guest ownership verification failed: {path}")
    shutil.copyfile(kernel_build / "kernel", out / "kernel")
    for path in [rootfs, out / "kernel"]:
        path.chmod(0o444)
    manifest = {
        "schema": 3, "bundle": bundle, "source_revision": run(
            ["git", "rev-parse", "HEAD"], cwd=REPO, capture_output=True, text=True).stdout.strip(),
        "pins": PINS, "kernel": kernel_manifest,
        "artifacts": {name: digest(out / name) for name in ["firecracker", "kernel", "rootfs.ext4"]},
        "guest_sha256": digest(stage / "poc/init"),
        "controller_sha256": digest(stage / "usr/local/bin/yoloctl"),
        "security_gate": "openshell-enforcement-v1",
        "scope": "mandatory OpenShell confinement probe + controller-read + empty system containerd + native ext4; no boxes or host shares",
    }
    save_json(out / "build.json", manifest)
    print(f"Prepared {out}\nRun: python3 {HERE / 'poc.py'} bench --build {out}", flush=True)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def api(path, method, endpoint, body, timeout=5):
    conn = UnixHTTP(path, timeout)
    try:
        conn.request(method, endpoint, json.dumps(body), {"Content-Type": "application/json"})
        response = conn.getresponse()
        payload = response.read(1 << 20)
        if response.status != 204:
            raise RuntimeError(f"Firecracker {method} {endpoint}: {response.status}: {payload.decode(errors='replace')}")
    finally:
        conn.close()


def guest(path, request, timeout):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.settimeout(timeout)
        conn.connect(str(path))
        with conn.makefile("rwb", buffering=0) as stream:
            stream.write(f"CONNECT {PORT}\n".encode())
            greeting = stream.readline(128)
            if not greeting.startswith(b"OK "):
                raise ConnectionError(f"vsock listener not ready: {greeting!r}")
            stream.write(json.dumps(request).encode() + b"\n")
            raw = stream.readline(65537)
            if len(raw) > 65536 or not raw.endswith(b"\n"):
                raise RuntimeError("truncated or oversized guest response")
            result = json.loads(raw)
            if result.get("nonce") != request["nonce"]:
                raise RuntimeError("guest response nonce mismatch")
            return result


def make_disk(path, fs_uuid=None):
    with path.open("xb") as stream:
        stream.truncate(DATA_SIZE)
    if fs_uuid:
        run(["mkfs.ext4", "-q", "-F", "-O", "^orphan_file", "-U", fs_uuid, "-E",
             "lazy_itable_init=0,lazy_journal_init=0,root_owner=0:0", path],
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def ms(start):
    return round((time.monotonic_ns() - start) / 1e6, 3)


def memory(pid):
    result = {}
    for line in Path(f"/proc/{pid}/smaps_rollup").read_text().splitlines():
        parts = line.split()
        if parts[0] in {"Rss:", "Pss:", "Private_Dirty:", "Shared_Clean:"}:
            result[parts[0][:-1] + "_kib"] = int(parts[1])
    return result


def guest_used_kib(meminfo):
    values = {}
    for line in meminfo.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[0] in {"MemTotal:", "MemAvailable:"} and parts[2] == "kB":
            values[parts[0][:-1]] = int(parts[1])
    return values["MemTotal"] - values["MemAvailable"]


def lima_baseline(directory):
    path = Path(directory).resolve() / "timings.jsonl"
    if path.stat().st_size > 1024 * 1024:
        raise RuntimeError("Lima timing report exceeds size limit")
    rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
    result = {"source": str(path), "sha256": digest(path),
              "scope": "historical Lima CLI lifecycle timings; not an equivalent capability/performance gate"}
    for mode in ["create", "start"]:
        samples = [float(row["seconds"]) * 1000 for row in rows
                   if (row["phase"] == mode or row["phase"].startswith(mode + "-")) and row["exit_code"] == 0]
        if samples:
            result[mode] = {"n": len(samples), "median_ms": statistics.median(samples), "samples_ms": samples}
    if "start" not in result:
        raise RuntimeError("Lima baseline has no successful startup timings")
    return result


class VM:
    def __init__(self, build, trial, disk, cpus, memory_mib, timeout):
        self.build, self.trial, self.disk = build, trial, disk
        self.cpus, self.memory_mib, self.timeout = cpus, memory_mib, timeout
        self.proc = None
        self.log = None
        self.directory = None
        self.start_ns = None

    def __enter__(self):
        try:
            # Linux sockaddr_un limits paths to 108 bytes; worktrees can be long.
            self.directory = Path(tempfile.mkdtemp(prefix="yfc-", dir="/tmp"))
            (self.directory / "data.ext4").symlink_to(self.disk)
            self.log = (self.trial / "serial.log").open("xb")
            self.start_ns = time.monotonic_ns()
            self.proc = subprocess.Popen([str(self.build / "firecracker"), "--api-sock", "api.sock"],
                                         cwd=self.directory, stdin=subprocess.DEVNULL,
                                         stdout=self.log, stderr=self.log)
            deadline = time.monotonic() + self.timeout
            while not (self.directory / "api.sock").exists():
                self.check_alive(deadline)
                time.sleep(0.002)
            return self
        except BaseException:
            self.__exit__(None, None, None)
            raise

    def check_alive(self, deadline):
        if self.proc.poll() is not None:
            raise RuntimeError(f"Firecracker exited {self.proc.returncode}; see {self.trial / 'serial.log'}")
        if time.monotonic() > deadline:
            raise TimeoutError(f"VM readiness timed out; see {self.trial / 'serial.log'}")

    def call(self, method, endpoint, body, timeout=None):
        api(self.directory / "api.sock", method, endpoint, body, timeout or self.timeout)

    def cold(self):
        self.call("PUT", "/machine-config", {"vcpu_count": self.cpus, "mem_size_mib": self.memory_mib})
        self.call("PUT", "/boot-source", {"kernel_image_path": str(self.build / "kernel"),
                  "boot_args": "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda ro init=/poc/init yoloctl.firecracker-poc=1"})
        self.call("PUT", "/drives/rootfs", {"drive_id": "rootfs", "path_on_host": str(self.build / "rootfs.ext4"),
                  "is_root_device": True, "is_read_only": True, "cache_type": "Writeback"})
        self.call("PUT", "/drives/data", {"drive_id": "data", "path_on_host": "data.ext4",
                  "is_root_device": False, "is_read_only": False, "cache_type": "Writeback"})
        self.call("PUT", "/vsock", {"guest_cid": 3, "uds_path": "vsock"})
        self.call("PUT", "/actions", {"action_type": "InstanceStart"})

    def restore(self, snapshot):
        # Relative data.ext4/vsock paths resolve inside this new private cwd.
        # The template has never mounted data.ext4 and all runtime writes live
        # in guest tmpfs. Rootfs is read-only and shared with the source VM.
        self.call("PUT", "/snapshot/load", {"snapshot_path": str(snapshot / "vm.state"),
                  "mem_backend": {"backend_type": "File", "backend_path": str(snapshot / "memory")},
                  "resume_vm": True})

    def ready(self, bundle):
        deadline = time.monotonic() + self.timeout
        while True:
            self.check_alive(deadline)
            try:
                response = guest(self.directory / "vsock", {"action": "status", "nonce": uuid.uuid4().hex}, 0.2)
            except (OSError, ConnectionError):
                time.sleep(0.005)
                continue
            if response.get("error"):
                raise RuntimeError(response["error"])
            if response.get("bundle") != bundle:
                raise RuntimeError("guest/controller build mismatch")
            if response.get("ok") and response.get("stage") == "runtime-ready":
                return
            time.sleep(0.005)

    def snapshot(self, destination):
        self.call("PATCH", "/vm", {"state": "Paused"})
        self.call("PUT", "/snapshot/create", {"snapshot_type": "Full",
                  "snapshot_path": str(destination / "vm.state"),
                  "mem_file_path": str(destination / "memory")}, timeout=180)
        for name in ["vm.state", "memory"]:
            with (destination / name).open("rb") as stream:
                os.fsync(stream.fileno())
        fd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def __exit__(self, exc_type, exc_value, traceback):
        if self.proc is not None:
            # Only our retained Popen handle; no process-name search or broad kill.
            if self.proc.poll() is None:
                self.proc.terminate()
                try:
                    self.proc.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    self.proc.kill()
                    self.proc.wait(timeout=3)
            self.proc.wait()
        if self.log is not None:
            self.log.close()
        if self.directory is not None:
            shutil.rmtree(self.directory)


def sample(args, build, manifest, output, name, snapshot=None):
    # Include exclusive directory creation and native disk formatting in the
    # request-to-ready figure, separately from launch-to-ready.
    begin = time.monotonic_ns()
    trial = new_directory(output / name)
    fs_uuid = str(uuid.uuid4())
    make_disk(trial / "data.ext4", fs_uuid)
    result = {"mode": "restore" if snapshot else "fresh", "disk_prepare_ms": ms(begin)}
    with VM(build, trial, trial / "data.ext4", args.cpus, args.memory_mib, args.timeout) as vm:
        if snapshot:
            vm.restore(snapshot)
        else:
            vm.cold()
        result["start_api_ms"] = ms(vm.start_ns)
        vm.ready(manifest["bundle"])
        result["runtime_ready_ms"] = ms(vm.start_ns)
        request = {"action": "activate", "nonce": uuid.uuid4().hex, "disk_id": uuid.uuid4().hex,
                   "disk_uuid": fs_uuid, "generation": uuid.uuid4().hex, "unix_nano": time.time_ns()}
        # Exactly one activation. A lost reply is a failed sample, never replayed.
        response = guest(vm.directory / "vsock", request, args.timeout)
        result["process_to_controller_ready_ms"] = ms(vm.start_ns)
        result["request_to_controller_ready_ms"] = ms(begin)
        validate_gate(response, request, manifest["bundle"])
        result["guest"] = response
        result["guest_used_kib_at_ready"] = guest_used_kib(response["meminfo"])
        result["host_vmm_at_ready"] = memory(vm.proc.pid)
        # Post-ready sampling is outside the readiness timer, includes every
        # restored page touched by the controller read (not just snapshot load).
        time.sleep(args.settle_seconds)
        vm.check_alive(time.monotonic() + 1)
        result["host_vmm_after_settle"] = memory(vm.proc.pid)
    save_json(trial / "result.json", result)
    print(f"{name}: request→controller {result['request_to_controller_ready_ms']:.1f} ms; "
          f"process→controller {result['process_to_controller_ready_ms']:.1f} ms", flush=True)
    return result


def validate_gate(response, request, bundle):
    if (not response.get("ok") or response.get("stage") != "controller-ready"
            or response.get("bundle") != bundle
            or response.get("nonce") != request["nonce"]
            or response.get("generation") != request["generation"]
            or response.get("accounts") != ["trial"]
            or response.get("landlock_abi", 0) < 3
            or response.get("openshell_confinement_passed") is not True):
        raise RuntimeError(f"OpenShell/controller/storage gate failed: {response}")


def bench(args):
    require_platform()
    require_tools(["mkfs.ext4"])
    try:
        fd = os.open("/dev/kvm", os.O_RDWR | os.O_CLOEXEC)
        os.close(fd)
    except OSError as exc:
        raise RuntimeError("/dev/kvm is unavailable to this user; run on your Linux host with KVM access") from exc
    if not 1 <= args.samples <= 100 or args.cpus < 1 or args.memory_mib < 256 or args.timeout <= 0 or args.settle_seconds < 0:
        raise RuntimeError("invalid samples/resources/timeout")
    build = Path(args.build).resolve()
    manifest = json.loads((build / "build.json").read_text())
    if (manifest.get("schema") != 3 or manifest.get("security_gate") != "openshell-enforcement-v1"
            or set(manifest["artifacts"]) != {"firecracker", "kernel", "rootfs.ext4"}):
        raise RuntimeError("unsupported/old build: prepare a new image with the mandatory OpenShell/Landlock gate")
    for name, expected in manifest["artifacts"].items():
        if digest(build / name) != expected:
            raise RuntimeError(f"build artifact changed: {name}; prepare a new build")
    output = new_directory(args.output or REPO / ("yfc-trial-" + uuid.uuid4().hex[:8]))
    print(f"Trial artifacts: {output}", flush=True)
    report = {"schema": 1, "status": "running", "build": manifest, "host": platform.uname()._asdict(),
              "cpus": args.cpus, "memory_mib": args.memory_mib,
              "scope": manifest["scope"], "samples": [], "cache_policy": "host caches left intact; artifacts verified before measurement",
              "goal_ms": 2000, "usable_space_proven": False}
    try:
        if args.lima_report:
            report["lima_baseline"] = lima_baseline(args.lima_report)
        snapshot = new_directory(output / "snapshot")
        make_disk(snapshot / "data.ext4")
        start = time.monotonic_ns()
        with VM(build, snapshot, snapshot / "data.ext4", args.cpus, args.memory_mib, args.timeout) as vm:
            vm.cold()
            vm.ready(manifest["bundle"])
            vm.snapshot(snapshot)
        report["template_prepare_ms"] = ms(start)
        report["snapshot_bytes"] = {p.name: {"logical": p.stat().st_size, "allocated": p.stat().st_blocks * 512}
                                    for p in [snapshot / "memory", snapshot / "vm.state"]}
        # Alternate order to reduce a systematic order/cache bias. No global
        # cache dropping, no host sysctl writes and no background warm VM pool.
        for index in range(args.samples):
            for mode in (["fresh", "restore"] if index % 2 == 0 else ["restore", "fresh"]):
                result = sample(args, build, manifest, output, f"{mode}-{index + 1:02}",
                                snapshot if mode == "restore" else None)
                report["samples"].append(result)
        report["summary"] = {}
        for mode in ["fresh", "restore"]:
            values = [r["request_to_controller_ready_ms"] for r in report["samples"] if r["mode"] == mode]
            report["summary"][mode] = {"n": len(values), "min_ms": min(values), "median_ms": statistics.median(values),
                                       "max_ms": max(values), "all_within_2s": max(values) <= 2000,
                                       "guest_used_median_mib": statistics.median(
                                           r["guest_used_kib_at_ready"] / 1024 for r in report["samples"] if r["mode"] == mode)}
        report["status"] = "passed-controller-gate"
    except BaseException as exc:
        report["status"], report["error"] = "failed", str(exc)
        raise
    finally:
        save_json(output / "report.json", report)
        print(f"Report: {output / 'report.json'}", flush=True)
    print(json.dumps(report["summary"], indent=2))
    print("PASS: controller/storage/runtime gate only; usable space/box readiness remains unmeasured.")


def software_smoke(args):
    """Functional kernel/OpenShell check; explicitly not a Firecracker timing."""
    require_platform()
    require_tools([args.qemu])
    if not args.security_only:
        require_tools(["mkfs.ext4"])
    build = Path(args.build).resolve()
    manifest = json.loads((build / "build.json").read_text())
    if manifest.get("schema") != 3 or manifest.get("security_gate") != "openshell-enforcement-v1":
        raise RuntimeError("old image lacks the mandatory OpenShell gate")
    for name in ["kernel", "rootfs.ext4"]:
        if digest(build / name) != manifest["artifacts"][name]:
            raise RuntimeError(f"build artifact changed: {name}")
    output = new_directory(args.output or REPO / ("yfc-software-" + uuid.uuid4().hex[:8]))
    print(f"Software smoke artifacts: {output}", flush=True)
    boot_args = "console=ttyS0 panic=-1 root=/dev/vda ro init=/poc/init yoloctl.firecracker-poc=1 yoloctl.software-smoke=1"
    request = {"action": "activate", "nonce": uuid.uuid4().hex, "disk_id": uuid.uuid4().hex,
               "disk_uuid": str(uuid.uuid4()), "generation": uuid.uuid4().hex, "unix_nano": time.time_ns()}
    if not args.security_only:
        make_disk(output / "data.ext4", request["disk_uuid"])
        boot_args += " yoloctl.full-smoke=1 yoloctl.activation=" + json.dumps(request).encode().hex()
    if args.trace:
        boot_args += " yoloctl.trace=1"
    argv = [args.qemu, "-machine", "q35,accel=tcg", "-cpu", "max", "-m", "1024", "-smp", "2",
            "-nodefaults", "-display", "none", "-serial", "stdio", "-no-reboot",
            "-kernel", str(build / "kernel"), "-append", boot_args,
            "-drive", f"file={build / 'rootfs.ext4'},format=raw,if=none,id=root,readonly=on",
            "-device", "virtio-blk-pci,drive=root"]
    if not args.security_only:
        argv += ["-drive", f"file={output / 'data.ext4'},format=raw,if=none,id=data,cache=writeback",
                 "-device", "virtio-blk-pci,drive=data"]
    if args.qemu_data:
        argv += ["-L", str(Path(args.qemu_data).resolve())]
    result = {"scope": "QEMU TCG guest kernel/OpenShell functional check; no Firecracker/KVM performance claim",
              "build_bundle": manifest["bundle"], "security_only": args.security_only, "status": "failed"}
    try:
        with (output / "serial.log").open("xb") as log:
            with subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=log, stderr=log) as proc:
                try:
                    proc.wait(timeout=args.timeout)
                finally:
                    if proc.poll() is None:
                        proc.kill()
                        proc.wait()
        raw = (output / "serial.log").read_bytes()
        marker = b"POC_SOFTWARE_SECURITY_PASS" if args.security_only else b"POC_SOFTWARE_CONTROLLER_PASS "
        if proc.returncode != 0 or marker not in raw:
            raise RuntimeError(f"software security smoke failed; inspect {output / 'serial.log'}")
        if not args.security_only:
            response = json.loads(raw.split(marker)[-1].splitlines()[0])
            validate_gate(response, request, manifest["bundle"])
            result["guest"] = response
        result["status"] = "passed"
    finally:
        save_json(output / "report.json", result)
        print(f"Software smoke report: {output / 'report.json'}", flush=True)
    print("PASS: guest kernel/OpenShell enforcement under QEMU TCG; Firecracker boot/restore remains untested.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    kern = sub.add_parser("kernel", help="build the pinned guest kernel with Landlock and OpenShell nftables support")
    kern.add_argument("--output", default=str(REPO / "yfc-kernel-ready"))
    kern.add_argument("--cache", help="optional directory of pinned downloads")
    kern.add_argument("--jobs", type=int, default=2)
    kern.set_defaults(func=kernel)
    prep = sub.add_parser("prepare", help="build private artifacts without KVM or root")
    prep.add_argument("--output", default=str(REPO / "yfc-build-ready"), help="new directory; never overwritten")
    prep.add_argument("--cache", help="optional directory containing the exact pinned downloads")
    prep.add_argument("--kernel-build", default=str(REPO / "yfc-kernel-ready"))
    prep.add_argument("--unsquashfs", default="unsquashfs", help="path to an existing unsquashfs binary")
    prep.set_defaults(func=prepare)
    trial = sub.add_parser("bench", help="boot/restore only disposable VMs; requires /dev/kvm")
    trial.add_argument("--build", default=str(REPO / "yfc-build-ready"))
    trial.add_argument("--output", help="new directory for reports/disks/snapshot; autogenerated by default")
    trial.add_argument("--lima-report", help="read an existing startup report's timings.jsonl for historical context")
    trial.add_argument("--samples", type=int, default=5)
    trial.add_argument("--cpus", type=int, default=4)
    trial.add_argument("--memory-mib", type=int, default=4096)
    trial.add_argument("--timeout", type=float, default=60)
    trial.add_argument("--settle-seconds", type=float, default=1)
    trial.set_defaults(func=bench)
    smoke = sub.add_parser("software-smoke", help="QEMU TCG kernel/OpenShell functional check without KVM; not a benchmark")
    smoke.add_argument("--build", default=str(REPO / "yfc-build-ready"))
    smoke.add_argument("--output", help="new report directory")
    smoke.add_argument("--qemu", default="qemu-system-x86_64")
    smoke.add_argument("--qemu-data", help="optional QEMU firmware directory for a private QEMU build")
    smoke.add_argument("--timeout", type=float, default=120)
    smoke.add_argument("--security-only", action="store_true", help="check kernel/OpenShell only, skipping controller/native disk")
    smoke.add_argument("--trace", action="store_true", help="include bounded guest strace diagnostics; never benchmark this mode")
    smoke.set_defaults(func=software_smoke)
    args = parser.parse_args()
    try:
        args.func(args)
    except (RuntimeError, OSError, subprocess.SubprocessError, ValueError) as exc:
        parser.exit(1, f"ERROR: {exc}\n")


if __name__ == "__main__":
    main()
