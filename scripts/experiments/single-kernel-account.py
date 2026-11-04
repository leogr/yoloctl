#!/usr/bin/env python3
"""Host-only fallback experiment: two confined boxes, one virtiofs guest mount.

This is not a cross-VM test or a production backend. Everything uses a fresh
isolated trial root. No personal accounts, credentials, or model calls are used.
Use --prepare-only inside the development box; never boot its enclosing VM.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import sys


spec = importlib.util.spec_from_file_location(
    "guest_image", Path(__file__).with_name("guest-image.py"))
guest_image = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guest_image)

BOXES = ("gi-account-a", "gi-account-b")
OLD_FILES = {"same-size": b"before01", "replace": b"before02", "recreate": b"before03"}
NEW_FILES = {"same-size": b"after001", "replace": b"after002", "recreate": b"after003",
             "new-name": b"after004"}

# All workloads enter through `box exec`, hence the OpenShell policy path.
HANDOFF = r"""import json, os, sys
from pathlib import Path
root, mode, expected = Path(sys.argv[1]), sys.argv[2], json.loads(sys.argv[3])
if mode == 'create':
    root.mkdir(mode=0o700)
    for name, content in expected.items():
        with (root / name).open('xb') as stream:
            stream.write(bytes.fromhex(content)); stream.flush(); os.fsync(stream.fileno())
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(directory)
    finally: os.close(directory)
actual = {p.name: p.read_bytes().hex() for p in root.iterdir()}
assert actual == expected, (actual, expected)
print(json.dumps({'files': actual, 'mode': mode}, sort_keys=True))
"""


def trial_template(source, image):
    template = guest_image.trial_template(source, image)
    before = 'mountType: "9p"'
    if template.count(before) != 1:
        raise ValueError("transport template changed; review the fallback experiment")
    return template.replace(before, 'mountType: "virtiofs"')


def isolated_directory(path, root):
    """Never mutate a path obtained from CLI output outside this fresh root."""
    candidate = Path(path)
    if not candidate.is_absolute() or candidate.is_symlink():
        raise ValueError("expected an absolute, ordinary isolated directory")
    resolved = candidate.resolve(strict=True)
    if resolved == root or not resolved.is_relative_to(root) or not resolved.is_dir():
        raise ValueError("directory is outside the isolated trial root")
    return resolved


def contents(root):
    result = {}
    for entry in root.iterdir():
        descriptor = os.open(entry, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            result[entry.name] = stream.read(1024)
    return result


def host_edit(root, trial_root):
    """Called only after both synthetic boxes stop. Never touch native auth."""
    root = isolated_directory(root, trial_root)
    if contents(root) != OLD_FILES:
        raise RuntimeError("handoff input differs; refusing host edits")
    original = (root / "same-size").stat()
    # Preserve size and mtime deliberately: freshness must not be inferred
    # solely from a changed length or a changed timestamp.
    descriptor = os.open(root / "same-size", os.O_WRONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(NEW_FILES["same-size"])
        stream.flush()
        os.fsync(stream.fileno())
    os.utime(root / "same-size", ns=(original.st_atime_ns, original.st_mtime_ns),
             follow_symlinks=False)
    temporary = root / "replacement-tmp"
    with temporary.open("xb") as stream:
        stream.write(NEW_FILES["replace"])
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(root / "replace")
    (root / "recreate").unlink()
    for name in ("recreate", "new-name"):
        with (root / name).open("xb") as stream:
            stream.write(NEW_FILES[name])
            stream.flush()
            os.fsync(stream.fileno())
    descriptor = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)
    if contents(root) != NEW_FILES:
        raise RuntimeError("host edits did not produce the expected synthetic files")


class Trial(guest_image.Trial):
    def __init__(self, image):
        super().__init__(image)
        self.report.update(scope="single-guest-kernel-two-openshell-boxes",
                           transport="virtiofs", host_handoff_pass=None,
                           pending_gates=["separate-VM sharing", "blocking lock cancellation",
                                          "crash and power-loss recovery", "mmap versus truncation",
                                          "production account maintenance fencing",
                                          "host workdir concurrency", "shared-VM isolation and repair UX",
                                          "authenticated agent resume", "macOS/VZ"])

    def host_load(self, label):
        self.report.setdefault("host_load", {})[label] = {
            "loadavg": Path("/proc/loadavg").read_text().strip(),
            "pressure": {kind: Path(f"/proc/pressure/{kind}").read_text()
                         for kind in ("cpu", "io", "memory")
                         if Path(f"/proc/pressure/{kind}").exists()}}
        self.save()

    def box(self, label, name, *argv, **kwargs):
        return self.cli(label, "box", "exec", name, "--space", guest_image.SPACE,
                        "--", *argv, **kwargs)

    def handoff(self, source, destination):
        path = destination + "/handoff"
        before = json.dumps({k: v.hex() for k, v in OLD_FILES.items()})
        after = json.dumps({k: v.hex() for k, v in NEW_FILES.items()})
        self.box("handoff-create", BOXES[0], "python3", "-c", HANDOFF, path, "create", before)
        for name in BOXES:
            self.box(f"handoff-prime-{name}", name, "python3", "-c", HANDOFF, path, "read", before)
        # These are the only boxes in this disposable VM; no managed agent was
        # launched. This does not implement general account maintenance leases.
        for name in BOXES:
            self.cli(f"handoff-stop-{name}", "box", "stop", name, "--space", guest_image.SPACE)
        self.report["handoff_vm_remained_running"] = True
        host_edit(source / "handoff", self.root)
        passed = True
        for name in BOXES:
            self.cli(f"handoff-start-{name}", "box", "start", name, "--space", guest_image.SPACE,
                     "--no-attach", "--no-autoupdate", timeout=900)
            code = self.box(f"handoff-read-{name}", name, "python3", "-c", HANDOFF,
                            path, "read", after, allowed=(0, 1))
            passed = passed and code == 0
        self.report["host_handoff_pass"] = passed
        self.report["persistence_witness"] = {
            "directory": str(source / "handoff"),
            "sha256": {name: hashlib.sha256(data).hexdigest() for name, data in NEW_FILES.items()}}
        self.save()

    def experiment(self):
        self.host_load("before-create")
        self.run("host-kernel", ["uname", "-a"])
        self.run("lima-version", ["limactl", "--version"])
        qemu = "qemu-system-aarch64" if platform.machine() == "aarch64" else "qemu-system-x86_64"
        self.run("qemu-version", [qemu, "--version"])
        self.started = True
        self.cli("fresh-space-create", "space", "create", guest_image.SPACE,
                 "--workdir", self.workdir, timeout=1800)
        self.cli("space-show", "space", "show", guest_image.SPACE, "--json")
        info = json.loads((self.root / "space-show.log").read_text())
        config = Path(info["config_path"]).resolve(strict=True)
        if not config.is_relative_to(self.root):
            raise RuntimeError("space config is outside the isolated trial root")
        shutil.copyfile(config, self.root / "resolved-lima.yaml")
        self.cli("guest-inventory", "space", "ssh", guest_image.SPACE, "--root", "--",
                 "bash", "-c", guest_image.INVENTORY)
        for sample in range(1, 4):
            self.host_load(f"before-vm-start-{sample}")
            self.cli(f"vm-stop-{sample}", "space", "stop", guest_image.SPACE)
            self.cli(f"cached-vm-start-{sample}", "space", "start", guest_image.SPACE, timeout=900)
        self.cli("account-create", "account", "create", guest_image.ACCOUNT)
        endpoints, bindings = [], []
        for name in BOXES:
            self.cli(f"create-{name}", "box", "create", name, "--space", guest_image.SPACE,
                     "--agent", "codex", "--account", guest_image.ACCOUNT,
                     "--workdir", self.workdir, "--no-attach", "--no-autoupdate", timeout=1800)
            self.cli(f"show-{name}", "box", "show", name, "--space", guest_image.SPACE, "--json")
            binding = json.loads((self.root / f"show-{name}.log").read_text())["account_state"]
            source = isolated_directory(binding["source"], self.root)
            destination = binding["destination"]
            if not binding["enabled"] or not destination.startswith("/home/sandbox/"):
                raise RuntimeError("unexpected account bind")
            bindings.append((source, destination))
            self.box(f"inventory-{name}", name, "sh", "-c",
                     'set -eu; codex --version; cat /proc/self/status; '
                     'grep -Eq "^Seccomp:[[:space:]]+2$" /proc/self/status; '
                     'findmnt -T "$1"; test "$(findmnt -n -o FSTYPE -T "$1")" = virtiofs',
                     "trial", destination)
            endpoints.append({"name": name, "root": destination + "/contract",
                              "prefix": [str(self.binary), "box", "exec", name,
                                         "--space", guest_image.SPACE, "--"]})
        if bindings[0] != bindings[1]:
            raise RuntimeError("boxes do not share the same account leaf")
        source, destination = bindings[0]
        self.box("contract-root", BOXES[0], "mkdir", "-m", "700", destination + "/contract")
        config = self.root / "endpoints.json"
        config.write_text(json.dumps(endpoints, indent=2) + "\n")
        self.host_load("before-contract")
        code = self.run("storage-contract", [sys.executable,
                        Path(__file__).with_name("storage-contract.py"), "--endpoints", config,
                        "--report", self.root / "storage-contract.json"], timeout=300, allowed=(0, 1))
        result = json.loads((self.root / "storage-contract.json").read_text())
        peers = list(result["endpoints"].values())
        if len(peers) != 2 or peers[0]["boot_id"] != peers[1]["boot_id"]:
            raise RuntimeError("probe did not establish a common guest kernel")
        if peers[0]["pid_namespace"] == peers[1]["pid_namespace"]:
            raise RuntimeError("probe unexpectedly shared a PID namespace")
        if any(peer["filesystem"] != "virtiofs" for peer in peers):
            raise RuntimeError("probe did not use virtiofs")
        self.report["storage_pass"] = code == 0 and result["status"] == "passed"
        if self.report["storage_pass"]:
            benchmark = Path(__file__).with_name("storage-microbench.py").read_text()
            self.host_load("before-file-measurements")
            for label, root in (("account-virtiofs", destination), ("container-local", "/tmp")):
                self.box(f"file-io-{label}", BOXES[0], "python3", "-c", benchmark,
                         "--root", root, "--label", label, timeout=120)
            self.report["file_measurements"] = ["file-io-account-virtiofs.log",
                                                "file-io-container-local.log"]
        self.handoff(source, destination)
        self.host_load("after-handoff")

    def verify_persistence(self):
        witness = self.report.get("persistence_witness")
        if not witness or self.report.get("cleanup_errors"):
            return
        directory = isolated_directory(witness["directory"], self.root)
        actual = {name: hashlib.sha256(data).hexdigest() for name, data in contents(directory).items()}
        self.report["account_survived_space_destroy"] = actual == witness["sha256"]
        if not self.report["account_survived_space_destroy"]:
            self.report["status"] = "persistence-failed"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prepare-only", action="store_true", help="build only; never create a VM")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "aarch64"):
        parser.error("this experiment targets Linux amd64/arm64 with QEMU + virtiofs")
    required = ["go", "git"]
    if not args.prepare_only:
        required += ["limactl", "ssh", "du",
                     "qemu-system-aarch64" if platform.machine() == "aarch64" else "qemu-system-x86_64"]
    missing = [tool for tool in required if not shutil.which(tool)]
    if missing:
        parser.error("missing host tools: " + ", ".join(missing) + "; run on the host or use --prepare-only")
    if not args.prepare_only and (os.geteuid() == 0 or not os.access("/dev/kvm", os.R_OK | os.W_OK)):
        parser.error("run as your normal Linux host user with access to /dev/kvm")
    # Lima discovers virtiofsd through QEMU vhost-user descriptors, not simply
    # PATH. Let Lima report discovery errors; never install/replace a daemon.
    signal.signal(signal.SIGTERM, guest_image.interrupt)
    os.umask(0o077)
    trial = Trial("minimal-26.04")
    try:
        trial.prepare(template_builder=trial_template)
        if args.prepare_only:
            trial.report["status"] = "prepared-not-booted"
        else:
            trial.experiment()
            trial.report["status"] = ("limited-gates-passed" if trial.report["storage_pass"]
                                      and trial.report["host_handoff_pass"] else "storage-gates-failed")
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        trial.report.update(status="failed", error=str(error) or "interrupted")
        print(f"Trial failed: {trial.report['error']}", flush=True)
    finally:
        try:
            trial.cleanup()
            trial.verify_persistence()
        except (OSError, ValueError, RuntimeError) as error:
            trial.report.update(status="cleanup-failed", cleanup_error=str(error))
        trial.save()
    print(f"Result: {trial.report['status']}; report: {trial.root / 'report.json'}", flush=True)
    return 0 if trial.report["status"] in ("prepared-not-booted", "limited-gates-passed") else 1


if __name__ == "__main__":
    raise SystemExit(main())
