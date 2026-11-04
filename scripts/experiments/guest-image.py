#!/usr/bin/env python3
"""Run the Ubuntu Minimal guest trial on a Linux host with Lima/QEMU.

The shipped template and container image are unchanged. A temporary Go build
overlay selects the candidate; every VM uses fresh XDG config/cache roots.
Logs remain under the printed /tmp directory; disposable VMs are removed.
Use --prepare-only to build the trial binary without Lima or VM operations.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


REPO = Path(__file__).resolve().parents[2]
SPACE = "gi"
BOX = "gi-probe"
ACCOUNT = "gi-probe"

INVENTORY = r"""set -eu
cat /etc/os-release
uname -a
systemctl --version
systemctl is-active containerd
nerdctl version
nerdctl info
/opt/openshell/bin/openshell-sandbox --version
for tool in bash sudo getent useradd groupadd curl socat ip ss timeout; do
    command -v "$tool"
done
cat /proc/meminfo
df -h /
findmnt -o TARGET,SOURCE,FSTYPE,OPTIONS
systemctl list-units --type=service --state=running --no-pager
systemctl --failed --no-pager
systemd-analyze time || true
systemd-analyze blame || true
cat /sys/kernel/security/lsm || true
grep -E 'CONFIG_(SECURITY_LANDLOCK|SECCOMP|SECCOMP_FILTER|NAMESPACES|CGROUPS|OVERLAY_FS|9P_FS|NET_9P_VIRTIO)=' /boot/config-"$(uname -r)" || true
cat /etc/netplan/*yaml
dpkg-query -W
"""


def trial_template(source, image):
    """Change only the image integration and freeze the Linux transport."""
    base = "base:\n- template:_images/ubuntu-24.04\n"
    if source.count(base) != 1:
        raise ValueError("shipped image template changed; review the experiment overlay")
    if image == "minimal-26.04":
        source = source.replace(base, Path(__file__).with_name(
            "ubuntu-minimal-images.yaml").read_text())
    # Lima defaults can change. The kernel comparison must keep cached 9p.
    for old, new in (
        ('{{ if .VMType }}vmType: "{{ .VMType }}"{{ end }}', 'vmType: "qemu"'),
        ('{{ if .MountType }}mountType: "{{ .MountType }}"{{ end }}', 'mountType: "9p"'),
    ):
        if source.count(old) != 1:
            raise ValueError("platform template changed; review the experiment overlay")
        source = source.replace(old, new)
    return source


class Trial:
    def __init__(self, image):
        # A short path avoids Unix-domain socket path limits. Never reuse an
        # existing root or honor inherited smoke/config/Lima directories.
        self.root = Path(tempfile.mkdtemp(prefix="ygi-", dir="/tmp"))
        self.env = os.environ.copy()
        for key in list(self.env):
            if key.startswith(("SMOKE_", "LIMA_")) or key == "YOLOCTL":
                del self.env[key]
        self.env.update(YOLOCTL_HOME=str(self.root / "home"), XDG_CONFIG_HOME=str(self.root / "config"),
                        XDG_CACHE_HOME=str(self.root / "cache"),
                        LIMA_CACHE_HOME=str(self.root / "image-cache"),
                        SMOKE_ROOT=str(self.root), SMOKE_SPACE=SPACE,
                        SMOKE_WORKDIR=str(self.root / "workdir"))
        self.workdir = self.root / "workdir"
        self.workdir.mkdir()
        self.binary = self.root / "yoloctl"
        self.env["YOLOCTL"] = str(self.binary)
        self.report = {"image": image, "root": str(self.root), "steps": [],
                       "status": "preparing", "storage_pass": None,
                       "pending_gates": ["concurrent truncate/read deadlock", "cross-VM locks/mmap",
                                         "SQLite WAL", "authenticated Codex exit/resume", "macOS/VZ"]}
        self.started = False
        print(f"Trial artifacts: {self.root}", flush=True)

    def save(self):
        # Rewrite, never O_RDWR|O_APPEND (the development workdir is affected).
        (self.root / "report.json").write_text(json.dumps(self.report, indent=2) + "\n")

    def run(self, label, command, timeout=300, allowed=(0,)):
        log = self.root / f"{label}.log"
        print(f">>> {label} (log: {log})", flush=True)
        start = time.monotonic()
        with log.open("wb") as output:
            proc = subprocess.Popen([str(x) for x in command], cwd=REPO,
                                    env=self.env, stdin=subprocess.DEVNULL,
                                    stdout=output, stderr=subprocess.STDOUT,
                                    start_new_session=True)
            try:
                code = proc.wait(timeout=timeout)
            except (subprocess.TimeoutExpired, KeyboardInterrupt):
                # Stop host SSH/limactl descendants as well as the CLI. The
                # finally block below also tears down the disposable VMs.
                os.killpg(proc.pid, signal.SIGKILL)
                proc.wait()
                self.report["steps"].append({"name": label, "interrupted": True,
                                             "seconds": round(time.monotonic() - start, 3),
                                             "log": str(log)})
                self.save()
                raise
        self.report["steps"].append({"name": label, "seconds": round(time.monotonic() - start, 3),
                                     "exit_code": code, "log": str(log)})
        self.save()
        if code not in allowed:
            print(log.read_text(errors="replace")[-6000:], flush=True)
            raise RuntimeError(f"{label} failed (exit {code}); see {log}")
        print(f"    exit {code}, {self.report['steps'][-1]['seconds']}s", flush=True)
        return code

    def cli(self, label, *args, **kwargs):
        return self.run(label, [self.binary, *args], **kwargs)

    def prepare(self, template_builder=trial_template):
        template_path = REPO / "assets/lima/yoloctl.yaml.tmpl"
        source = template_path.read_text()
        template = template_builder(source, self.report["image"])
        backing = self.root / "trial.yaml.tmpl"
        backing.write_text(template)
        overlay = self.root / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(template_path): str(backing)}}))
        self.run("build", ["go", "build", "-overlay", overlay, "-o", self.binary,
                           "./cmd/yoloctl"], timeout=900)
        # Verify go:embed actually consumed the overlay, not the shipped file.
        if template.encode() not in self.binary.read_bytes():
            raise RuntimeError("trial binary did not embed the candidate template")
        if template_path.read_text() != source:
            raise RuntimeError("shipped template changed during preparation")
        self.report["template_sha256"] = hashlib.sha256(template.encode()).hexdigest()
        self.run("source-revision", ["git", "rev-parse", "HEAD"])
        self.run("source-status", ["git", "status", "--short"])
        self.run("go-version", ["go", "version"])

    def experiment(self):
        self.run("host-kernel", ["uname", "-a"])
        self.run("lima-version", ["limactl", "--version"])
        qemu = "qemu-system-aarch64" if platform.machine() == "aarch64" else "qemu-system-x86_64"
        self.run("qemu-version", [qemu, "--version"])
        self.started = True  # Also clean up if creation/provisioning fails.
        self.cli("fresh-space-create", "space", "create", SPACE,
                 "--workdir", self.workdir, timeout=1800)
        self.cli("space-show", "space", "show", SPACE, "--json")
        info = json.loads((self.root / "space-show.log").read_text())
        config = Path(info["config_path"]).resolve()
        if not config.is_relative_to(self.root):
            raise RuntimeError("space is outside the isolated trial root")
        shutil.copyfile(config, self.root / "resolved-lima.yaml")
        self.run("disk-after-provision", ["du", "-sk", info["dir"]])
        self.cli("guest-inventory", "space", "ssh", SPACE, "--root", "--",
                 "bash", "-c", INVENTORY)
        # Restart before any boxes exist, then use session-aware box stop/start
        # for container timings. All samples keep normal provisioning enabled.
        for sample in range(1, 4):
            self.cli(f"vm-stop-{sample}", "space", "stop", SPACE)
            self.cli(f"cached-vm-start-{sample}", "space", "start", SPACE, timeout=900)
        self.cli("account-create", "account", "create", ACCOUNT)
        self.cli("probe-box-create", "box", "create", BOX, "--space", SPACE,
                 "--agent", "codex", "--account", ACCOUNT, "--workdir", self.workdir,
                 "--no-attach", "--no-autoupdate", timeout=1800)
        self.cli("probe-box-inspect", "box", "inspect", BOX, "--space", SPACE, "--json")
        self.cli("probe-box-show", "box", "show", BOX, "--space", SPACE, "--json")
        box = json.loads((self.root / "probe-box-show.log").read_text())
        workdir = box["workdir"]["container_path"]
        account_dir = box["account_state"]["destination"]
        self.cli("account-show", "account", "show", ACCOUNT, "--json")
        state_home = json.loads((self.root / "account-show.log").read_text())["state_home"]
        self.cli("guest-test-mounts", "space", "ssh", SPACE, "--root", "--", "bash", "-c",
                 'set -eu; for path in "$@"; do '
                 'findmnt -T "$path"; test "$(findmnt -n -o FSTYPE -T "$path")" = 9p; '
                 'opts=$(findmnt -n -o OPTIONS -T "$path"); '
                 'case ",$opts," in *,cache=0x5,*|*,cache=mmap,*) ;; '
                 '*) echo "Expected cached 9p, got $opts" >&2; exit 1;; esac; done',
                 "trial", self.workdir, state_home)
        self.cli("container-inventory", "box", "exec", BOX, "--space", SPACE, "--",
                 "sh", "-c", "set -eu; cat /etc/os-release; codex --version; pwd; "
                 "cat /proc/self/status; grep -Eq '^Seccomp:[[:space:]]+2$' /proc/self/status; "
                 "for path in \"$@\"; do findmnt -T \"$path\"; "
                 "test \"$(findmnt -n -o FSTYPE -T \"$path\")\" = 9p; done",
                 "trial", workdir, account_dir)
        self.cli("container-image-inventory", "space", "ssh", SPACE, "--root", "--",
                 "nerdctl", "images", "--digests", "--no-trunc")
        probe = (REPO / "scripts/diagnose/append-integrity.py").read_text()
        code = self.cli("append-integrity", "box", "exec", BOX, "--space", SPACE,
                        "--", "python3", "-c", probe, "/tmp", workdir, account_dir,
                        allowed=(0, 1), timeout=120)
        self.report["storage_pass"] = code == 0
        self.port_probe()
        # A known baseline mismatch must not prevent collecting lifecycle data.
        for sample in range(1, 4):
            self.cli(f"box-stop-{sample}", "box", "stop", BOX, "--space", SPACE)
            self.cli(f"cached-box-start-{sample}", "box", "start", BOX, "--space", SPACE,
                     "--no-attach", "--no-autoupdate", timeout=300)
            self.cli(f"box-exec-ready-{sample}", "box", "exec", BOX, "--space", SPACE,
                     "--", "true")
        self.cli("probe-box-destroy", "box", "destroy", BOX, "--space", SPACE, "--force")
        self.cli("probe-account-destroy", "account", "destroy", ACCOUNT, "--force")
        # Run phases separately to retain timings and each phase's log. The
        # account-state phase creates and cleans up gi-b as well as gi.
        self.env["SMOKE_PRESERVE_SPACE"] = "1"
        for phase in ("space-lifecycle", "image-ensure", "features-as-policy",
                      "agent-state", "box-attach"):
            self.run(f"smoke-{phase}", ["bash", REPO / f"scripts/smoke/{phase}.sh"],
                     timeout=3600)

    def port_probe(self):
        # Launch the server through box exec, inside OpenShell. Keep its SSH
        # client alive until session-aware box stop terminates the server.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        token = self.root.name.encode()
        server = ("from http.server import BaseHTTPRequestHandler, HTTPServer\n"
                  "class Handler(BaseHTTPRequestHandler):\n"
                  " def do_GET(self):\n"
                  f"  self.send_response(200); self.end_headers(); self.wfile.write({token!r})\n"
                  f"HTTPServer(('0.0.0.0', {port}), Handler).serve_forever()\n")
        with (self.root / "port-server.log").open("wb") as log:
            proc = subprocess.Popen([str(self.binary), "box", "exec", BOX, "--space", SPACE,
                                     "--", "python3", "-c", server],
                                    env=self.env, stdin=subprocess.DEVNULL, stdout=log,
                                    stderr=subprocess.STDOUT, start_new_session=True)
            try:
                self.cli("port-forward", "port", "forward", BOX, str(port), "--space", SPACE)
                deadline = time.monotonic() + 45
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                while True:
                    try:
                        with opener.open(f"http://127.0.0.1:{port}/", timeout=2) as response:
                            if response.read() != token:
                                raise RuntimeError("forwarded port returned unexpected content")
                        break
                    except (urllib.error.URLError, OSError):
                        if proc.poll() is not None or time.monotonic() >= deadline:
                            raise RuntimeError("port forward did not reach the sandbox server") from None
                        time.sleep(1)
                self.cli("port-list", "port", "list", BOX, "--space", SPACE, "--json")
                self.cli("port-unforward", "port", "unforward", BOX, str(port), "--space", SPACE)
                self.cli("port-list-after", "port", "list", BOX, "--space", SPACE, "--json")
                if json.loads((self.root / "port-list-after.log").read_text()):
                    raise RuntimeError("port forward is still listed after removal")
                self.report["port_pass"] = True
            finally:
                try:
                    self.cli("port-box-stop", "box", "stop", BOX, "--space", SPACE)
                finally:
                    if proc.poll() is None:
                        os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()

    def cleanup(self):
        if not self.started:
            return
        failed = []
        # Fixed names refer only to this newly allocated config root. Keep the
        # root and logs even if VM deletion fails; never rm -rf a live Lima home.
        for space in (SPACE + "-b", SPACE):
            try:
                self.cli(f"cleanup-list-{space}", "box", "list", "--space", space, "--json",
                         timeout=30)
                boxes = json.loads((self.root / f"cleanup-list-{space}.log").read_text())
                for box in boxes:
                    self.cli(f"cleanup-box-{space}-{box['name']}", "box", "destroy", box["name"],
                             "--space", space, "--force", timeout=60)
            except (OSError, ValueError, RuntimeError, subprocess.TimeoutExpired) as error:
                # If the guest is wedged, host-side forced VM deletion is the
                # recovery path. Normal cleanup goes through session teardown.
                self.report.setdefault("cleanup_warnings", []).append(str(error))
            try:
                self.cli(f"cleanup-{space}", "space", "destroy", space,
                         "--force", "--purge", timeout=120)
            except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
                failed.append(str(error))
        self.report["cleanup_errors"] = failed
        if failed:
            self.report["status"] = "cleanup-failed"
            print("Cleanup incomplete; retain the trial root and inspect cleanup logs.", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", choices=("minimal-26.04", "ubuntu-24.04"),
                        default="minimal-26.04", help="candidate (default) or current-template baseline")
    parser.add_argument("--prepare-only", action="store_true", help="build only; never create a VM")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "aarch64"):
        parser.error("this first trial targets Linux amd64/arm64 with QEMU + 9p")
    required = ["go", "git"]
    if not args.prepare_only:
        required += ["limactl", "ssh", "script", "du", "timeout",
                     "qemu-system-aarch64" if platform.machine() == "aarch64" else "qemu-system-x86_64"]
    missing = [tool for tool in required if not shutil.which(tool)]
    if missing:
        parser.error("missing host tools: " + ", ".join(missing) + "; run on the host, or use --prepare-only")
    if not args.prepare_only and os.geteuid() == 0:
        parser.error("run as your normal host user, not root (Lima requires this)")
    signal.signal(signal.SIGTERM, interrupt)
    os.umask(0o077)
    trial = Trial(args.image)
    try:
        trial.prepare()
        if args.prepare_only:
            trial.report["status"] = "prepared-not-booted"
        else:
            trial.experiment()
            trial.report["status"] = "first-stage-passed" if trial.report["storage_pass"] else "append-failed"
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.TimeoutExpired, KeyboardInterrupt) as error:
        trial.report.update(status="failed", error=str(error) or "interrupted")
        print(f"Trial failed: {trial.report['error']}", flush=True)
    finally:
        trial.cleanup()
        trial.save()
    print(f"Result: {trial.report['status']}; report: {trial.root / 'report.json'}", flush=True)
    return 0 if trial.report["status"] in ("prepared-not-booted", "first-stage-passed") else 1


def interrupt(_signum, _frame):
    raise KeyboardInterrupt


if __name__ == "__main__":
    raise SystemExit(main())
