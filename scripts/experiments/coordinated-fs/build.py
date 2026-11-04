#!/usr/bin/env python3
"""Build a pinned Linux x86_64 synthetic prototype in a fresh private directory.

No sudo, package installation, VM operation, production data or global Rust
cache is used. Requires Python >=3.12, C/kernel build tools and Rust >=1.85.
The unoptimized build is for correctness checks, not performance comparisons.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
PINS = json.loads((HERE / "pins.json").read_text())


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def source(root, name, cache):
    pin = PINS[name]
    archive = root / pin["archive"]
    cached = cache / pin["archive"] if cache else None
    if cached and cached.is_file():
        shutil.copyfile(cached, archive)
    else:
        with urllib.request.urlopen(pin["url"], timeout=60) as response, archive.open("xb") as output:
            shutil.copyfileobj(response, output)
    if digest(archive) != pin["sha256"]:
        raise RuntimeError(f"{name}: source checksum differs from the reviewed pin")
    staging = root / (name + "-extract")
    staging.mkdir()
    with tarfile.open(archive) as tar:
        tar.extractall(staging, filter="data")
    entries = list(staging.iterdir())
    if len(entries) != 1 or not entries[0].is_dir():
        raise RuntimeError("unexpected archive layout")
    destination = root / name
    entries[0].rename(destination)
    staging.rmdir()
    return destination


def execute(argv, cwd, env, log):
    print("Running: " + " ".join(map(str, argv)), flush=True)
    with log.open("ab") as output:
        result = subprocess.run(list(map(str, argv)), cwd=cwd, env=env,
                                stdout=output, stderr=subprocess.STDOUT, timeout=1800)
    if result.returncode:
        raise RuntimeError(f"build command failed ({result.returncode}); see {log}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, help="new directory; defaults to a fresh /var/tmp/ycf-build-*")
    parser.add_argument("--cache", type=Path, help="optional directory of pinned source archives; checksums still required")
    parser.add_argument("--jobs", type=int, default=2)
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        parser.error("this bounded prototype only supports Linux x86_64 with 4 KiB pages")
    if os.sysconf("SC_PAGESIZE") != 4096 or not 1 <= args.jobs <= 8:
        parser.error("requires 4 KiB pages and 1..8 build jobs")
    for tool in ("cc", "make", "patch", "rustc", "cargo", "bison", "flex", "bc", "m4", "pkg-config"):
        if not shutil.which(tool):
            parser.error(f"missing {tool}; install the host's build prerequisites before running")
    if args.directory:
        args.directory.mkdir(mode=0o700, parents=False)
        root = args.directory.resolve()
    else:
        root = Path(tempfile.mkdtemp(prefix="ycf-build-", dir="/var/tmp"))
    print("Build directory: " + str(root), flush=True)
    log = root / "build.log"
    env = os.environ.copy()
    env.update(CARGO_HOME=str(root / "cargo-home"), CARGO_BUILD_JOBS=str(args.jobs),
               CARGO_PROFILE_DEV_DEBUG="0", KBUILD_BUILD_TIMESTAMP="2026-09-25 00:00:00 UTC",
               KBUILD_BUILD_USER="ycf-prototype", KBUILD_BUILD_HOST="synthetic")
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "pins": PINS, "status": "failed", "limits": "Build and native component test only; no VM qualification."}
    try:
        for tool in ("cc", "rustc", "cargo"):
            report[tool] = subprocess.check_output([tool, "--version"], text=True).splitlines()[0]
        krun = source(root, "libkrun", args.cache)
        firmware = source(root, "libkrunfw", args.cache)
        kernel = source(root, "kernel", args.cache)
        for patch in sorted((firmware / "patches").glob("0*.patch")):
            execute(["patch", "--batch", "--fuzz=0", "-p1", "-i", patch], kernel, env, log)
        execute(["patch", "--batch", "--fuzz=0", "-p1", "-i", HERE / "kernel.patch"], kernel, env, log)
        execute(["patch", "--batch", "--fuzz=0", "-p1", "-i", HERE / "libkrun.patch"], krun, env, log)
        shutil.copyfile(HERE / "libkrun-coordinator.rs", krun / "src/devices/src/virtio/fs/linux/coordinated.rs")
        shutil.copyfile(HERE / "native-filesystem.rs", krun / "src/devices/src/virtio/fs/linux/coordinated_native_fs.rs")
        (krun / "src/devices/examples").mkdir(exist_ok=True)
        shutil.copyfile(HERE / "native-worker.rs", krun / "src/devices/examples/ycf-native.rs")
        shutil.copyfile(firmware / "config-libkrunfw_x86_64", kernel / ".config")
        execute(["make", "olddefconfig"], kernel, env, log)
        execute(["make", f"-j{args.jobs}", "vmlinux"], kernel, env, log)
        execute(["cargo", "test", "--locked", "-p", "krun-devices",
                 "virtio::fs::linux::coordinated::tests"], krun, env, log)
        execute(["cargo", "build", "--locked", "-p", "krun-devices", "--features", "test_utils",
                 "--example", "ycf-native"], krun, env, log)
        execute(["python3", HERE / "native-control.py", "--worker", krun / "target/debug/examples/ycf-native",
                 "--report", root / "native-control.json"], root, env, log)
        execute(["python3", HERE / "native-control.py", "--worker", krun / "target/debug/examples/ycf-native",
                 "--filesystem", "--report", root / "native-filesystem.json"], root, env, log)
        execute(["cargo", "build", "--locked", "-p", "libkrun"], krun, env, log)
        artifacts = root / "artifacts"
        for name in ("bin", "lib", "kernel"):
            (artifacts / name).mkdir(parents=True)
        shutil.copyfile(krun / "target/debug/libkrun.so", artifacts / "lib/libkrun.so")
        (artifacts / "lib/libkrun.so.1").symlink_to("libkrun.so")
        shutil.copyfile(kernel / "vmlinux", artifacts / "kernel/vmlinux")
        execute(["cc", "-O2", "-Wall", "-Wextra", "-Werror", "-isystem", krun / "include",
                 HERE / "vmm.c", "-L", artifacts / "lib", "-lkrun",
                 "-Wl,-rpath,$ORIGIN/../lib", "-o", artifacts / "bin/ycf-vmm"], root, env, log)
        report["artifacts"] = {str(f.relative_to(root)): digest(f) for f in artifacts.rglob("*")
                               if f.is_file() and not f.is_symlink()}
        report["kernel_config_sha256"] = digest(kernel / ".config")
        report["experiment_sources"] = {f.name: digest(f) for f in HERE.iterdir() if f.is_file()}
        report["status"] = "built"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
        raise
    finally:
        (root / "build-report.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Built; host trial command: python3 " + str(HERE / "trial.py") + " --build " + str(root))


if __name__ == "__main__":
    main()
