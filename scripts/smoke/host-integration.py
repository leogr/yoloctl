#!/usr/bin/env python3
"""Isolated Linux live transport test with deterministic desktop stand-ins.

Uses real host frontends/controller/holder/OpenShell SSH, while replacing only
the final desktop clipboard reader/opener. Never reads the caller's clipboard
or opens an actual browser. Native desktop/agent UX needs manual acceptance.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import struct
import subprocess
import sys
import zlib

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("attach_mixed", Path(__file__).with_name("attach-mixed.py"))
mixed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mixed)


def png_fixture():
    def chunk(kind, data):
        return struct.pack("!I", len(data)) + kind + data + struct.pack("!I", zlib.crc32(kind + data))
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack("!2I5B", 1, 1, 8, 6, 0, 0, 0)) +
            chunk(b"IDAT", zlib.compress(b"\0\xff\x80\0\xff")) + chunk(b"IEND", b""))


def events(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_bytes().split(b"\n")[:-1]]


def run(driver, workdir):
    root = driver.output
    clipboard = root / "clipboard.png"
    clipboard.write_bytes(png_fixture())
    reference = root / "clipboard-reference"
    opened = root / "opened.jsonl"
    backend = root / "desktop"
    backend.mkdir()
    reader = (f"#!{sys.executable}\nimport os,sys\nfrom pathlib import Path\n"
              "p=Path(os.environ['YOLOCTL_SMOKE_CLIPBOARD'])\n"
              "r=Path(os.environ['YOLOCTL_SMOKE_REFERENCE'])\n"
              "kind='text/uri-list' if r.exists() else 'image/png'\n"
              "if not (r.exists() or p.exists()): sys.exit(1)\n"
              "if '--list-types' in sys.argv: print(kind)\n"
              "elif sys.argv[-1] == kind: sys.stdout.buffer.write((r if r.exists() else p).read_bytes())\n"
              "else: sys.exit(1)\n")
    opener = (f"#!{sys.executable}\nimport os,sys,json\n"
              "with open(os.environ['YOLOCTL_SMOKE_OPENED'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n")
    for name, source in (("wl-paste", reader), ("xdg-open", opener)):
        path = backend / name
        path.write_text(source)
        path.chmod(0o700)
    os.environ.update(PATH=str(backend) + os.pathsep + os.environ["PATH"],
                      WAYLAND_DISPLAY="yoloctl-smoke", YOLOCTL_SMOKE_CLIPBOARD=str(clipboard),
                      YOLOCTL_SMOKE_REFERENCE=str(reference),
                      YOLOCTL_SMOKE_OPENED=str(opened))
    os.environ["YOLOCTL_HOME"] = str(Path(os.environ["SMOKE_ROOT"]) / "home")
    config = Path(json.loads(subprocess.check_output([driver.yoloctl, "version", "--json"]))["config_root"]) / "tui.yaml"
    config.write_text("seams:\n  browser: ask\n  clipboard: true\n")
    audit = workdir / "host-integration-audit.jsonl"
    source = Path(__file__).with_name("host-integration-probe.py").read_bytes()
    # The shell's cwd is the host project mirror; only this isolated workdir is
    # read by the driver. Stage the probe through confined exec before attaching.
    command = driver.prepare_probe(source, "host-integration-audit.jsonl", driver.token)
    managed = driver.start("managed", driver.attach)
    driver.wait("shell", lambda: b"sandbox@" in managed.tail, timeout=60)
    driver.send(managed, command)
    driver.wait("confined probe", lambda: any(e["event"] == "ready" for e in events(audit)))
    if not events(audit)[0]["browser"]:
        raise mixed.Failure("session browser helper was not installed")

    tui = driver.start("tui", [driver.yoloctl])
    driver.wait("palette", lambda: b"yoloctl" in tui.tail)
    driver.send(tui, f"box attach {driver.box} --space {driver.space} --no-autoupdate\r".encode())
    marker = b"HOST_READY_" + driver.token.encode()
    driver.wait("TUI managed snapshot", lambda: marker in tui.tail)
    print(">>> host integration: explicit PNG paste in a TUI tab", flush=True)
    driver.send(tui, b"\x16")
    driver.wait("guest PNG", lambda: any(e["event"] == "image" for e in events(audit)))
    image = next(e for e in events(audit) if e["event"] == "image")
    if image["sha256"] != hashlib.sha256(png_fixture()).hexdigest():
        raise mixed.Failure("image bytes changed during upload")
    print(">>> host integration: reuse an accessible workdir image without copying", flush=True)
    shared = workdir / "already shared image.png"
    shared.write_bytes(png_fixture())
    reference.write_text(shared.resolve().as_uri() + "\r\n")
    driver.send(tui, b"\x16")
    driver.wait("reused PNG", lambda: len([e for e in events(audit) if e["event"] == "image"]) == 2)
    reused = [e for e in events(audit) if e["event"] == "image"][-1]
    if reused["path"] != "/workdir" + str(shared.resolve()) or reused["sha256"] != image["sha256"]:
        raise mixed.Failure("workdir image was copied or its contents changed")
    # A copied file outside this box's workdir needs a private upload.
    reference.write_text(clipboard.resolve().as_uri() + "\r\n")
    driver.send(tui, b"\x16")
    driver.wait("outside-workdir upload", lambda: len([e for e in events(audit) if e["event"] == "image"]) == 3)
    copied = [e for e in events(audit) if e["event"] == "image"][-1]
    if not copied["path"].startswith("/tmp/yoloctl-image-") or copied["sha256"] != image["sha256"]:
        raise mixed.Failure("outside-workdir file did not use the upload fallback")
    reference.unlink()
    clipboard.unlink()
    driver.send(tui, b"\x16")
    driver.wait("non-image Ctrl+V", lambda: any(e["event"] == "ctrl-v" for e in events(audit)))

    print(">>> host integration: one chosen window, confirmation and rejection", flush=True)
    driver.send(tui, b"b")
    driver.wait("browser prompt opens automatically", lambda: b"Open link in your browser?" in tui.tail)
    if opened.exists():
        raise mixed.Failure("browser opened before confirmation")
    driver.send(tui, b"\x0f")  # visible prompt: Ctrl+O
    driver.wait("approved browser result", lambda: any(e["event"] == "browser" for e in events(audit)))
    if next(e for e in events(audit) if e["event"] == "browser")["status"] != 0:
        raise mixed.Failure("approved helper failed")
    # Approval restores the original tab focus, without an extra Escape.
    driver.send(tui, b"i")
    driver.wait("invalid scheme rejected", lambda: any(e.get("request") == "i" for e in events(audit)))
    if next(e for e in events(audit) if e.get("request") == "i")["status"] == 0:
        raise mixed.Failure("helper accepted file URL")
    if len(opened.read_text().splitlines()) != 1:
        raise mixed.Failure("invalid URL reached desktop opener")
    tui.tail = b""
    driver.send(tui, b"b")
    driver.wait("new browser prompt", lambda: b"Open link in your browser?" in tui.tail)
    driver.wait("second browser choices", lambda: b"Ctrl+O Open" in tui.tail)
    driver.send(tui, b"\x1b[27u")
    driver.wait("declined browser result", lambda: len([e for e in events(audit) if e["event"] == "browser"]) == 3)
    if [e for e in events(audit) if e["event"] == "browser"][-1]["status"] == 0:
        raise mixed.Failure("declined helper reported success")
    print(">>> host integration: pending request canceled on detach; repeated prompts after TUI rejoin", flush=True)
    tui.tail = b""
    driver.send(tui, b"b")
    driver.wait("pending browser prompt", lambda: b"Open link in your browser?" in tui.tail)
    tui.terminate()
    driver.wait("TUI detached", lambda: tui.status is not None)
    driver.wait("disconnected request canceled", lambda: len([e for e in events(audit) if e["event"] == "browser"]) == 4)
    if [e for e in events(audit) if e["event"] == "browser"][-1]["status"] == 0 or len(opened.read_text().splitlines()) != 1:
        raise mixed.Failure("disconnect accepted or retried the pending browser request")
    # Another accepting view was present throughout, but may not inherit the
    # disconnected view's request. A subsequent explicit request can use it.
    if b"Open link in your browser?" in managed.tail:
        raise mixed.Failure("pending browser request was rerouted to another window")
    tui_rejoined = driver.start("tui-rejoined", [driver.yoloctl])
    driver.wait("rejoined palette", lambda: b"yoloctl" in tui_rejoined.tail)
    driver.send(tui_rejoined, f"box attach {driver.box} --space {driver.space} --no-autoupdate\r".encode())
    driver.wait("rejoined TUI snapshot", lambda: marker in tui_rejoined.tail)
    driver.send(tui_rejoined, b"p")
    driver.wait("rejoined TUI input", lambda: any(e["event"] == "ping" for e in events(audit)))
    if b"Open link in your browser?" in tui_rejoined.tail or len(opened.read_text().splitlines()) != 1:
        raise mixed.Failure("TUI reconnect replayed a browser request")
    for total, key, status, opens in ((5, b"\x0f", 0, 2), (6, b"\x1b[27u", 1, 2)):
        tui_rejoined.tail = b""
        driver.send(tui_rejoined, b"b")
        driver.wait("rejoined browser prompt", lambda: b"Open link in your browser?" in tui_rejoined.tail)
        driver.send(tui_rejoined, key)
        driver.wait("rejoined browser result", lambda: len([e for e in events(audit) if e["event"] == "browser"]) == total)
        if [e for e in events(audit) if e["event"] == "browser"][-1]["status"] != status or len(opened.read_text().splitlines()) != opens:
            raise mixed.Failure("rejoined browser request did not honor its own decision")
    driver.send(tui_rejoined, b"\x19\x17\x04")  # close tab and empty TUI
    driver.wait("rejoined TUI detached", lambda: tui_rejoined.status is not None)
    # A missing browser setting now opens automatically; preserve the explicit
    # ask behavior of the other, already-running attachment.
    config.write_text("seams:\n  clipboard: true\n")
    rejoined = driver.start("rejoined", driver.attach)
    driver.wait("reattach snapshot", lambda: marker in rejoined.tail)
    driver.send(rejoined, b"p")
    driver.wait("reattach input", lambda: len([e for e in events(audit) if e["event"] == "ping"]) == 2)
    if len(opened.read_text().splitlines()) != 2 or b"Open link in your browser?" in rejoined.tail:
        raise mixed.Failure("reattach replayed a browser action")
    driver.send(rejoined, b"b")
    driver.wait("browser opens by default", lambda: len([e for e in events(audit) if e["event"] == "browser"]) == 7)
    if [e for e in events(audit) if e["event"] == "browser"][-1]["status"] != 0 or len(opened.read_text().splitlines()) != 3:
        raise mixed.Failure("default browser policy did not open once")
    driver.send(rejoined, b"q")
    driver.wait("clean agent exit", lambda: rejoined.status is not None and managed.status is not None)
    if (managed.status, rejoined.status, tui.status, tui_rejoined.status) != (0, 0, 0, 0):
        raise mixed.Failure("host integration changed session exit status")
    print(">>> host integration: inherited BROWSER remains usable after holder restart", flush=True)
    retained_browser = events(audit)[0]["browser_path"]
    restarted_audit = workdir / "host-integration-restarted.jsonl"
    # Reuse the original environment value, as a surviving agent daemon does.
    # Local regressions also keep a real process alive across bridge crashes.
    command = driver.prepare_probe(source, restarted_audit.name, driver.token + "-restarted",
                                   env={"BROWSER": retained_browser})
    restarted = driver.start("holder-restarted", driver.attach)
    driver.wait("restarted shell", lambda: b"sandbox@" in restarted.tail, timeout=60)
    driver.send(restarted, command)
    driver.wait("restarted probe", lambda: any(e["event"] == "ready" for e in events(restarted_audit)))
    for count in (1, 2):
        driver.send(restarted, b"b")
        driver.wait("retained helper result", lambda: len([e for e in events(restarted_audit) if e["event"] == "browser"]) == count)
        if [e for e in events(restarted_audit) if e["event"] == "browser"][-1]["status"] != 0 or len(opened.read_text().splitlines()) != 3 + count:
            raise mixed.Failure("retained BROWSER failed or duplicated an action after holder restart")
    driver.send(restarted, b"q")
    driver.wait("restarted agent exit", lambda: restarted.status is not None)
    if restarted.status != 0:
        raise mixed.Failure("restarted agent did not exit cleanly")
    print(">>> PASS: confined browser helper, image transfer/reuse, fallback, approval, reconnect and holder restart", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("yoloctl", "space", "box", "output", "workdir"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    driver = mixed.Driver(str(Path(args.yoloctl).resolve()), args.space, args.box, args.output)
    def stop(signum, _frame):
        raise mixed.Failure(f"interrupted by signal {signum}")
    for signum in (signal.SIGINT, signal.SIGTERM):
        signal.signal(signum, stop)
    try:
        run(driver, Path(args.workdir))
        return 0
    except (mixed.Failure, OSError, ValueError) as error:
        print(f"FAIL: {error}; captures: {driver.output}", file=sys.stderr)
        for view in driver.views:
            print(f"{Path(view.capture.name).name}: {view.status}, tail={view.tail[-500:]!r}", file=sys.stderr)
        try:
            audit = (Path(args.workdir) / "host-integration-audit.jsonl").read_bytes()
            print(f"guest audit tail: {audit[-2000:]!r}", file=sys.stderr)
        except OSError:
            pass
        return 1
    finally:
        driver.close()


if __name__ == "__main__":
    sys.exit(main())
