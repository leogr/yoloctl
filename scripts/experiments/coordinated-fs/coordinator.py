#!/usr/bin/env python3
"""Private synthetic-trial coordinator; not a production account service.

Fixed 40-byte little-endian requests, 8-byte errno replies. One global gate
serializes mapping registration, resizing and ordinary writes. Reverse channels
must acknowledge a mapping change before the mutator may continue. Any lost
participant poisons the entire trial; the runner must kill and reap its VMMs.
"""

import argparse
import ctypes
import fcntl
import json
import os
from pathlib import Path
import socket
import signal
import struct
import threading
import time

FRAME = struct.Struct("<5Q")
REPLY = struct.Struct("<Q")
REGISTER, BEGIN, RESIZE, END = 1, 2, 3, 4


def receive(sock, length):
    result = bytearray()
    while len(result) < length:
        data = sock.recv(length - len(result))
        if not data:
            raise EOFError("participant disconnected")
        result.extend(data)
    return bytes(result)


class Coordinator:
    def __init__(self, root, timeout):
        self.root = root
        self.timeout = timeout
        self.gate = threading.Lock()
        self.peers = {}
        self.events = (root / "events.jsonl").open("x", buffering=1)
        self.log_lock = threading.Lock()
        # Stable inode, separate from yoloctl's account mutation lock.
        self.lease = os.open(root / "lifetime.lock", os.O_CREAT | os.O_RDWR, 0o600)
        fcntl.flock(self.lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fcntl.flock(self.lease, fcntl.LOCK_SH)

    def record(self, event, **fields):
        with self.log_lock:
            self.events.write(json.dumps({"event": event, "monotonic": time.monotonic(), **fields}) + "\n")

    def poison(self, reason):
        self.record("poison", reason=reason)
        # An exception in a thread is insufficient: terminate the authority and
        # every event connection, making the VMM listeners abort as well.
        os._exit(78)

    def connection(self, conn):
        held = False
        registered = False
        client = None
        try:
            conn.settimeout(self.timeout)
            op, client, dev, ino, size = FRAME.unpack(receive(conn, FRAME.size))
            if op == REGISTER:
                with self.gate:
                    if client in self.peers or len(self.peers) >= 2:
                        raise ValueError("duplicate or excess participant")
                    self.peers[client] = conn
                    registered = True
                    self.record("register", client=client)
                    conn.sendall(REPLY.pack(0))
                # Reverse channel has exactly one reader: the transaction
                # thread that broadcasts under the gate. Keep this thread
                # parked without reading acknowledgments from that channel.
                return
            if op != BEGIN:
                raise ValueError("expected BEGIN")
            if not self.gate.acquire(timeout=self.timeout):
                raise TimeoutError("operation gate timed out")
            held = True
            if client not in self.peers:
                raise ValueError("unregistered participant")
            conn.sendall(REPLY.pack(0))
            while True:
                op, sender, dev, ino, size = FRAME.unpack(receive(conn, FRAME.size))
                if sender != client:
                    raise ValueError("participant changed within transaction")
                if op == END:
                    conn.sendall(REPLY.pack(0))
                    break
                if op != RESIZE:
                    raise ValueError("expected RESIZE or END")
                for peer in self.peers.values():
                    peer.sendall(FRAME.pack(RESIZE, 0, dev, ino, size))
                    if REPLY.unpack(receive(peer, REPLY.size))[0]:
                        raise OSError("participant could not replace its mapping")
                self.record("resize_ack", client=client, dev=dev, ino=ino, size=size,
                            participants=len(self.peers))
                conn.sendall(REPLY.pack(0))
        except (OSError, EOFError, ValueError) as error:
            self.poison(type(error).__name__ + ": " + str(error))
        finally:
            if held:
                self.gate.release()
            if not registered:
                conn.close()

    def serve(self):
        path = self.root / "control.sock"
        listener = socket.socket(socket.AF_UNIX)
        listener.bind(str(path))  # Existing sockets are deliberately never removed.
        os.chmod(path, 0o600)
        listener.listen(8)
        self.record("ready", pid=os.getpid())
        while True:
            conn, _ = listener.accept()
            threading.Thread(target=self.connection, args=(conn,), daemon=True).start()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", type=Path, help="existing private synthetic trial directory")
    parser.add_argument("--timeout", type=float, default=10)
    parser.add_argument("--parent-pid", type=int, help="trial controller PID; terminate if it disappears")
    args = parser.parse_args()
    if not 0 < args.timeout <= 60:
        parser.error("timeout must be between 0 and 60 seconds")
    if args.root.stat().st_mode & 0o077:
        parser.error("trial directory must be private (mode 0700)")
    if args.parent_pid is not None:
        libc = ctypes.CDLL(None, use_errno=True)
        if libc.prctl(1, signal.SIGKILL, 0, 0, 0) or os.getppid() != args.parent_pid:
            parser.error("cannot bind coordinator lifetime to trial controller")
    Coordinator(args.root, args.timeout).serve()


if __name__ == "__main__":
    main()
