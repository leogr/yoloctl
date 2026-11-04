"""Measure a managed terminal response using a private PTY, never the caller's TTY."""
import errno
import fcntl
import os
from pathlib import Path
import pty
import select
import signal
import struct
import termios
import time


def measure(command, env, capture, marker, timeout):
    pid, master = pty.fork()
    if pid == 0:
        os.execvpe(command[0], command, dict(env, TERM='xterm-256color'))
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 100, 0, 0))
    reaped = False
    tail = b''
    try:
        with Path(capture).open('xb') as stream:
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                if select.select([master], [], [], min(.02, max(0, deadline-time.monotonic())))[0]:
                    try:
                        data = os.read(master, 65536)
                    except OSError as error:
                        if error.errno != errno.EIO:
                            raise
                        data = b''
                    if not data:
                        raise RuntimeError('terminal closed before confined readiness response')
                    stream.write(data)
                    tail = (tail + data)[-65536:]
                    if marker in tail:
                        ready = time.perf_counter_ns()
                        os.kill(pid, signal.SIGTERM)  # normal managed detach
                        for _ in range(250):
                            done, status = os.waitpid(pid, os.WNOHANG)
                            if done:
                                reaped = True
                                if os.waitstatus_to_exitcode(status) != 0:
                                    raise RuntimeError('managed detach failed')
                                return ready
                            time.sleep(.02)
                        raise RuntimeError('managed detach did not finish')
                done, _ = os.waitpid(pid, os.WNOHANG)
                if done:
                    reaped = True
                    raise RuntimeError('terminal exited before confined readiness response')
            raise TimeoutError('managed terminal readiness timed out; inspect the private capture')
    finally:
        if not reaped:
            try:
                os.killpg(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            os.waitpid(pid, 0)
        os.close(master)
