"""Read one chat artifact inside the confined chat runtime (yoloctl relay).

argv: CONFIG ARTIFACT_UUID EXPECTED_BYTES EXPECTED_SHA256

Runs only the fixed AIRC client: `airc artifact download --config CONFIG UUID
PATH` into a new private directory, as this process's one child in its own
process group, under a 4 MiB file-size limit. Standard input is a cancel-only
channel: end of input before success means cancel. The relay keeps its own
15 s deadline. On success (child exit 0 and reaped, bounded JSON naming the
expected size and hash, an owned no-follow regular 0600 file of exactly that
size and hash, private directory removed) it writes a 4-byte big-endian length
and the bytes to standard output and exits 0. Every failure writes nothing to
standard output and exits nonzero with one short line on standard error.
"""

import errno
import hashlib
import json
import os
import resource
import selectors
import signal
import stat
import struct
import subprocess
import sys
import tempfile
import time

AIRC = "/usr/local/bin/airc"
MAX_BYTES = 4 << 20
DEADLINE = 15.0
OUTPUT_LIMIT = 8 << 10
TERM_GRACE = 0.5
REAP_LIMIT = 1.5


class Failure(Exception):
    pass


def fail(message, code=1):
    sys.stderr.write("artifact relay: " + message + "\n")
    sys.stderr.flush()
    sys.exit(code)


def parse_args(argv):
    if len(argv) != 5:
        raise Failure("usage")
    config, artifact, size, digest = argv[1:]
    if len(artifact) != 36 or any(
        (c != "-" if i in (8, 13, 18, 23) else c not in "0123456789abcdef")
        for i, c in enumerate(artifact)
    ):
        raise Failure("invalid artifact id")
    if not size.isdigit() or str(int(size)) != size or not 0 < int(size) <= MAX_BYTES:
        raise Failure("invalid size")
    if len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
        raise Failure("invalid hash")
    if not config.startswith("/") or "\0" in config:
        raise Failure("invalid config")
    return config, artifact, int(size), digest


def limit_file_size():
    resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_BYTES, MAX_BYTES))


class Child:
    """The one download child: started in a new process group, reaped once."""

    def __init__(self, argv):
        self.proc = subprocess.Popen(
            argv,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
            preexec_fn=limit_file_size,
            close_fds=True,
        )
        self.status = None

    def poll(self):
        if self.status is None:
            self.status = self.proc.poll()
        return self.status

    def stop(self):
        """TERM the owned group, then KILL; wait a bounded time to reap."""
        if self.poll() is not None:
            return True
        try:
            os.killpg(self.proc.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        end = time.monotonic() + TERM_GRACE
        while time.monotonic() < end:
            if self.poll() is not None:
                return True
            time.sleep(0.02)
        try:
            os.killpg(self.proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        end = time.monotonic() + REAP_LIMIT
        while time.monotonic() < end:
            if self.poll() is not None:
                return True
            time.sleep(0.02)
        return False


def run_download(config, artifact, path, deadline):
    """Runs the child until it exits, input ends, output overflows or the
    deadline passes. Returns its stdout on exit 0."""
    child = Child([AIRC, "artifact", "download", "--config", config, artifact, path])
    sel = selectors.DefaultSelector()
    outputs = {child.proc.stdout: bytearray(), child.proc.stderr: bytearray()}
    for f in outputs:
        os.set_blocking(f.fileno(), False)
        sel.register(f, selectors.EVENT_READ)
    stdin = sys.stdin.buffer
    sel.register(stdin, selectors.EVENT_READ)
    reason = None
    try:
        while True:
            left = deadline - time.monotonic()
            if left <= 0:
                reason = "deadline"
                break
            for key, _ in sel.select(timeout=min(left, 0.1)):
                if key.fileobj is stdin:
                    if not os.read(stdin.fileno(), 4096):
                        reason = "canceled"
                        break
                    continue
                chunk = os.read(key.fileobj.fileno(), 4096)
                if not chunk:
                    sel.unregister(key.fileobj)
                    continue
                buf = outputs[key.fileobj]
                buf.extend(chunk)
                if len(buf) > OUTPUT_LIMIT:
                    reason = "output too large"
                    break
            if reason:
                break
            if child.poll() is not None and len(sel.get_map()) == 1:
                break  # exited and both outputs closed
    finally:
        sel.close()
    if reason:
        if not child.stop():
            raise Failure(reason + "; download child not reaped")
        raise Failure(reason)
    if child.poll() != 0:
        raise Failure("download failed")
    return bytes(outputs[child.proc.stdout])


def verify(path, reply, size, digest):
    try:
        result = json.loads(reply)
    except ValueError:
        raise Failure("invalid download reply")
    if not isinstance(result, dict) or result.get("bytes") != size or result.get("sha256") != digest:
        raise Failure("download reply does not match the artifact")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | getattr(os, "O_CLOEXEC", 0))
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o600:
            raise Failure("unexpected download file")
        data = b""
        while len(data) <= size:
            chunk = os.read(fd, size + 1 - len(data))
            if not chunk:
                break
            data += chunk
    finally:
        os.close(fd)
    if len(data) != size or hashlib.sha256(data).hexdigest() != digest:
        raise Failure("downloaded bytes do not match the artifact")
    return data


def cleanup(directory, path):
    """Removes exactly the relay's file and directory."""
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass
    os.rmdir(directory)


def send(data, deadline):
    """Writes the frame without blocking past the deadline; input ending
    while writing aborts it."""
    frame = struct.pack(">I", len(data)) + data
    out = sys.stdout.buffer.fileno()
    os.set_blocking(out, False)
    sel = selectors.DefaultSelector()
    sel.register(out, selectors.EVENT_WRITE)
    sel.register(sys.stdin.buffer, selectors.EVENT_READ)
    try:
        while frame:
            left = deadline - time.monotonic()
            if left <= 0:
                raise Failure("reply not read in time")
            for key, _ in sel.select(timeout=min(left, 0.1)):
                if key.fileobj is sys.stdin.buffer:
                    if not os.read(sys.stdin.buffer.fileno(), 4096):
                        raise Failure("canceled during reply")
                    continue
                try:
                    n = os.write(out, frame)
                except BlockingIOError:
                    continue
                frame = frame[n:]
    finally:
        sel.close()


def main(argv):
    start = time.monotonic()
    deadline = start + DEADLINE
    signal.signal(signal.SIGPIPE, signal.SIG_DFL)
    try:
        config, artifact, size, digest = parse_args(argv)
    except Failure as e:
        fail(str(e), 2)
    directory = tempfile.mkdtemp(prefix="yolo-artifact-", dir="/tmp")
    os.chmod(directory, 0o700)
    path = os.path.join(directory, "artifact")
    try:
        reply = run_download(config, artifact, path, deadline)
        data = verify(path, reply, size, digest)
    except Failure as e:
        try:
            cleanup(directory, path)
        except OSError:
            fail(str(e) + "; private download left at " + directory)
        fail(str(e))
    except OSError as e:
        try:
            cleanup(directory, path)
        except OSError:
            fail("download error; private download left at " + directory)
        fail("download error: " + (errno.errorcode.get(e.errno, "") or "io"))
    try:
        cleanup(directory, path)
    except OSError:
        fail("cleanup failed; private download left at " + directory)
    try:
        send(data, start + DEADLINE + 3.0)
    except Failure as e:
        fail(str(e))
    sys.exit(0)


if __name__ == "__main__":
    main(sys.argv)
