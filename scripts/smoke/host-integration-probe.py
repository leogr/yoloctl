#!/usr/bin/env python3
"""Guest-side audit for image paths and browser helpers; no desktop access."""
import hashlib
import json
import os
from pathlib import Path
import select
import shlex
import subprocess
import sys
import termios
import tty


def main():
    audit, token = Path(sys.argv[1]), sys.argv[2]

    def record(event, **values):
        with audit.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(dict(event=event, **values)) + "\n")

    state = termios.tcgetattr(0)
    browser = None
    request = None
    pending = b""
    tty.setraw(0)
    try:
        os.write(1, ("\x1b[?2004hHOST_READY_" + token + "\r\n").encode())
        record("ready", browser=bool(os.environ.get("BROWSER")), browser_path=os.environ.get("BROWSER", ""))
        while True:
            if browser is not None and browser.poll() is not None:
                record("browser", request=request, status=browser.returncode)
                browser = None
            ready, _, _ = select.select([0], [], [], 0.02)
            if not ready:
                continue
            data = os.read(0, 8192)
            if not data:
                return
            pending += data
            while pending:
                if pending.startswith(b"\x1b[200~"):
                    end = pending.find(b"\x1b[201~")
                    if end < 0:
                        break
                    paths = shlex.split(pending[6:end].decode())
                    if len(paths) != 1:
                        raise RuntimeError("image paste is not one path")
                    path = paths[0]
                    payload = Path(path).read_bytes()
                    record("image", path=path, sha256=hashlib.sha256(payload).hexdigest())
                    pending = pending[end + 6:]
                    continue
                if b"\x1b[200~".startswith(pending):
                    break
                key, pending = pending[0], pending[1:]
                if key in (ord("b"), ord("i")):
                    if browser is not None:
                        raise RuntimeError("overlapping browser request")
                    request = chr(key)
                    url = "https://example.org/yoloctl-smoke" if key == ord("b") else "file:///tmp/forbidden"
                    browser = subprocess.Popen([os.environ["BROWSER"], url],
                                               stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                               stderr=subprocess.DEVNULL)
                elif key == 22:
                    record("ctrl-v")
                elif key == ord("p"):
                    record("ping")
                elif key == ord("q"):
                    record("done")
                    return
                else:
                    raise RuntimeError(f"unexpected key {key}")
    finally:
        if browser is not None:
            browser.kill()
            browser.wait()
        os.write(1, b"\x1b[?2004l")
        termios.tcsetattr(0, termios.TCSANOW, state)


if __name__ == "__main__":
    main()
