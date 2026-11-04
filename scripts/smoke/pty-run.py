#!/usr/bin/env python3
"""Run a Linux/macOS smoke command on a PTY, retaining output and exit status.

Stdin is finite scripted input, sent once a lifecycle command announces its
attach. Keep the PTY open after input EOF so detach tests retain a live shell.
The output file retains raw bytes. Stdout is an escaped diagnostic log, never
a terminal proxy: the caller's terminal must not execute the child's controls.
"""

import argparse
import errno
import fcntl
import os
import pty
import select
import signal
import struct
import sys
import termios
import time


_LOG_BYTES = tuple(bytes([c]) if c == 10 or 32 <= c < 127 else f"\\x{c:02x}".encode("ascii")
                   for c in range(256))


def diagnostic_output(data):
    """Keep printable ASCII/newlines; escape controls and non-ASCII bytes.

    Bytewise escaping stays safe across read boundaries, including split UTF-8
    and C1 controls. Raw capture remains the source for all smoke assertions.
    """
    return b"".join(_LOG_BYTES[c] for c in data)


def read_pty(master):
    """Both Linux EIO and macOS EOF mean the slave has closed."""
    try:
        return os.read(master, 65536)
    except OSError as exc:
        if exc.errno == errno.EIO:
            return b""
        raise


def drain_pty(master, emit):
    """Retain bytes queued at child exit without waiting on inherited slaves."""
    os.set_blocking(master, False)
    deadline = time.monotonic() + 0.25
    while time.monotonic() < deadline:
        try:
            data = read_pty(master)
        except BlockingIOError:
            break
        if not data:
            break
        emit(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--pid-file")
    parser.add_argument("--timeout", type=float, default=900)
    parser.add_argument("--ready-text", default="notice: attaching to box")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        parser.error("a command is required after --")
    pending = sys.stdin.buffer.read()
    pid, master = pty.fork()
    if pid == 0:
        os.environ.setdefault("TERM", "xterm-256color")
        os.execvp(command[0], command)
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 100, 0, 0))
    if args.pid_file:
        with open(args.pid_file, "w", encoding="ascii") as f:
            f.write(f"{pid}\n")

    def stop(signum, _frame):
        raise SystemExit(128 + signum)

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    deadline = time.monotonic() + args.timeout
    status = None
    tail = b""
    send_at = None
    try:
        with open(args.output, "wb", buffering=0) as capture:
            def emit(data):
                capture.write(data)
                sys.stdout.buffer.write(diagnostic_output(data))
                sys.stdout.buffer.flush()

            eof = False
            while time.monotonic() < deadline:
                readable, _, _ = select.select([] if eof else [master], [], [], 0.1)
                if readable:
                    data = read_pty(master)
                    if not data:
                        eof = True
                    emit(data)
                    tail = (tail + data)[-4096:]
                    if pending and send_at is None and args.ready_text.encode() in tail:
                        # The notice precedes terminal setup. Allow raw mode
                        # to be established before sending scripted input.
                        send_at = time.monotonic() + 1
                # Always reap, even while a descendant is still producing
                # output. Drain after reaping: the child may have written
                # between the select sample and waitpid.
                reaped, child_status = os.waitpid(pid, os.WNOHANG)
                if reaped:
                    status = child_status
                    drain_pty(master, emit)
                    break
                if not eof and pending and send_at is not None and time.monotonic() >= send_at:
                    sent = os.write(master, pending)
                    pending = pending[sent:]
            else:
                print("PTY smoke command timed out", file=sys.stderr)
                return 124
        code = os.waitstatus_to_exitcode(status)
        return code if code >= 0 else 128 - code
    finally:
        if status is None:
            try:
                os.killpg(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            os.waitpid(pid, 0)
        os.close(master)


if __name__ == "__main__":
    sys.exit(main())
