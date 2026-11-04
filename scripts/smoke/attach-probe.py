#!/usr/bin/env python3
"""Guest workload for attach-mixed.py; stdlib only, no extra PTY allocation.

The audit file is independent of rendered output. Three disjoint input alphabets
let the driver check each sender's byte order without imposing a global order on
concurrent writers. Only the holder should answer our cursor-position queries.
"""

import json
import os
import re
import select
import signal
import sys
import termios
import time
import tty


def run(audit_path, token, timeout=300):
    saved = termios.tcgetattr(0)
    old_blocking = os.get_blocking(1)
    old_winch = signal.getsignal(signal.SIGWINCH)
    resized = True
    pending = bytearray()
    incoming = bytearray()
    flooding = False
    exiting = False
    flood_bytes = 0
    reported_bytes = 0
    chunks = 0
    next_chunk = 0.0
    deadline = time.monotonic() + timeout

    def winch(_signum, _frame):
        nonlocal resized
        resized = True

    with open(audit_path, "w", encoding="ascii", buffering=1) as audit:
        def record(event, **fields):
            audit.write(json.dumps(dict(event=event, **fields)) + "\n")

        try:
            tty.setraw(0)
            os.set_blocking(1, False)
            signal.signal(signal.SIGWINCH, winch)
            pending.extend(b"\x1b[2J\x1b[HMIXED_READY_" + token.encode("ascii") + b"\r\n")
            record("ready", token=token, pid=os.getpid())
            while time.monotonic() < deadline:
                if resized:
                    # Clear before sampling: a signal during the size query
                    # or audit write must leave another sample pending.
                    resized = False
                    size = os.get_terminal_size(0)
                    record("resize", cols=size.columns, rows=size.lines, flooding=flooding)
                if flooding and not pending and time.monotonic() >= next_chunk:
                    if flood_bytes >= 128 * 1024 * 1024:
                        raise RuntimeError("output budget exceeded before driver stopped the flood")
                    # Rewrite one short row instead of growing history. Change
                    # its counter so managed renderers must paint fresh output;
                    # identical frames could legitimately produce no bytes.
                    row = f"\r\x1b[2K0123456789 {chunks:08x} abcdefghijklmnopqrstuv".encode("ascii")
                    chunk = row * 512
                    chunks += 1
                    pending.extend(chunk)
                    flood_bytes += len(chunk)
                    if flood_bytes - reported_bytes >= 1024 * 1024:
                        record("progress", bytes=flood_bytes)
                        reported_bytes = flood_bytes
                    next_chunk = time.monotonic() + 0.02
                readers, writers, _ = select.select([0], [1] if pending else [], [], 0.01)
                if readers:
                    data = os.read(0, 65536)
                    if not data:
                        raise RuntimeError("agent stdin closed unexpectedly")
                    incoming.extend(data)
                    while incoming:
                        if incoming[0] == 27:
                            reply = re.match(rb"\x1b\[(\d+);(\d+)R", incoming)
                            if reply:
                                record("reply", row=int(reply[1]), col=int(reply[2]))
                                del incoming[:reply.end()]
                                continue
                            if len(incoming) < 32 and re.fullmatch(rb"\x1b(?:\[(?:\d*(?:;\d*)?)?)?", incoming):
                                break
                            raise RuntimeError(f"unexpected terminal input {bytes(incoming[:32])!r}")
                        key = chr(incoming.pop(0))
                        if key in "abcDEF123":
                            record("input", key=key, flooding=flooding)
                        elif key == "?":
                            record("query", flooding=flooding)
                            pending.extend(b"\x1b[2;3H\x1b[6n")
                        elif key == "+":
                            flooding = True
                            record("flood-start")
                        elif key == "-":
                            flooding = False
                            record("flood-stop", bytes=flood_bytes)
                        elif key == "!":
                            flooding = False
                            exiting = True
                            pending.extend(b"\r\nMIXED_DONE\r\n")
                        else:
                            raise RuntimeError(f"unexpected input byte {ord(key):02x}")
                if writers and pending:
                    try:
                        written = os.write(1, pending)
                        del pending[:written]
                    except BlockingIOError:
                        pass
                if exiting and not pending:
                    record("done", bytes=flood_bytes)
                    return 7
            raise RuntimeError("guest probe timed out")
        except Exception as error:
            record("error", message=str(error))
            raise
        finally:
            signal.signal(signal.SIGWINCH, old_winch)
            os.set_blocking(1, old_blocking)
            termios.tcsetattr(0, termios.TCSANOW, saved)


if __name__ == "__main__":
    sys.exit(run(sys.argv[1], sys.argv[2]))
