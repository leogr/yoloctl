"""Box-local browser helper, served by one live holder through OpenShell SSH.

Only bounded URL requests and their results cross this pipe. No host paths,
clipboard reads, shell commands, or administrative services are exposed.
"""
import fcntl
import json
import os
import select
import socket
import stat
import sys
import tempfile
import time

MAX_LINE = 2 * 8192 + 1024

HELPER = r'''#!/usr/bin/env python3
import json, os, socket, sys
try:
    if len(sys.argv) != 2 or len(sys.argv[1].encode()) > 8192:
        raise ValueError("expected one HTTP(S) URL")
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(100)
    sock.connect(os.path.join(os.path.dirname(os.path.realpath(__file__)), "browser.sock"))
    sock.sendall(json.dumps({"url": sys.argv[1]}, ensure_ascii=False).encode() + b"\n")
    with sock.makefile("rb") as stream:
        line = stream.readline(2048)
    if not line:
        raise ValueError("browser request interrupted; no retry was made")
    result = json.loads(line)
    if result.get("error"):
        raise ValueError(result["error"])
except (FileNotFoundError, ConnectionRefusedError):
    print("yoloctl browser: no active managed session for this box", file=sys.stderr)
    sys.exit(1)
except Exception as e:
    print("yoloctl browser: " + str(e), file=sys.stderr)
    sys.exit(1)
'''


def emit(value):
    sys.stdout.write(json.dumps(value, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def publish_helper(name):
    fd, temporary = tempfile.mkstemp(prefix=".helper-", dir=".")
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            output.write(HELPER)
            os.fchmod(output.fileno(), 0o500)
        # Replace the entry, never follow a pre-existing helper symlink.
        os.replace(temporary, name)
    finally:
        if os.path.lexists(temporary):
            os.unlink(temporary)


def remove_socket():
    try:
        info = os.lstat("browser.sock")
    except FileNotFoundError:
        return
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.getuid():
        raise ValueError("unsafe browser socket")
    os.unlink("browser.sock")


def serve(directory):
    # The holder supplies a constant box-local path. Tests supply a private
    # directory so they never touch a running session's endpoint. Keep the
    # directory, entry points and lock inode across holders: daemons may retain
    # BROWSER/PATH after the launching session has gone away.
    try:
        os.mkdir(directory, 0o700)
    except FileExistsError:
        pass
    directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        info = os.fstat(directory_fd)
        if info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o700:
            raise ValueError("unsafe browser directory")
        os.fchdir(directory_fd)
    finally:
        os.close(directory_fd)
    lock = os.open("bridge.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
    info = os.fstat(lock)
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or
            stat.S_IMODE(info.st_mode) != 0o600 or info.st_nlink != 1):
        raise ValueError("unsafe browser lock")
    # An old SSH channel may still be closing. Bound the wait within the
    # holder's startup timeout. A competing bridge must never unlink its socket.
    deadline = time.monotonic() + 3
    while True:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            break
        except BlockingIOError:
            if time.monotonic() >= deadline:
                raise ValueError("another browser bridge is still active")
            time.sleep(0.02)
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    active = None
    bound = False
    try:
        publish_helper("open-url")
        publish_helper("xdg-open")
        remove_socket()
        server.bind("browser.sock")
        bound = True
        server.listen(4)
        emit({"directory": directory})
        # Unbuffered binary stdin avoids select missing an already-buffered line.
        incoming = bytearray()
        while True:
            ready, _, _ = select.select([server, sys.stdin.buffer.raw], [], [])
            if sys.stdin.buffer.raw in ready:
                chunk = os.read(0, MAX_LINE + 1)
                if not chunk:
                    return
                incoming.extend(chunk)
                if len(incoming) > MAX_LINE:
                    return
                if b"\n" in incoming:
                    line, _, incoming = incoming.partition(b"\n")
                    result = json.loads(line)
                    if active is not None:
                        try:
                            active.sendall(json.dumps(result).encode() + b"\n")
                        except OSError:
                            pass
                        active.close()
                        active = None
            if server in ready:
                conn, _ = server.accept()
                if active is not None:
                    conn.sendall(b'{"error":"another browser request is pending"}\n')
                    conn.close()
                    continue
                conn.settimeout(2)
                try:
                    with conn.makefile("rb") as stream:
                        line = stream.readline(MAX_LINE + 1)
                    if len(line) > MAX_LINE or not line.endswith(b"\n"):
                        raise ValueError("invalid request size")
                    request = json.loads(line)
                    if set(request) != {"url"} or not isinstance(request["url"], str):
                        raise ValueError("invalid request")
                    emit(request)
                    active = conn
                except (OSError, ValueError, TypeError):
                    conn.close()
    finally:
        if active is not None:
            active.close()
        server.close()
        if bound:
            remove_socket()
        os.close(lock)


serve(sys.argv[1])
