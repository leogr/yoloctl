import fcntl
import importlib.util
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest

SPEC = importlib.util.spec_from_file_location("coordinator", Path(__file__).with_name("coordinator.py"))
COORD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(COORD)


class CoordinatorTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="ycf-protocol-", dir="/tmp")
        self.root = Path(self.temp.name)
        self.log = (self.root / "stderr").open("wb")
        self.process = subprocess.Popen([sys.executable, str(Path(COORD.__file__)), str(self.root),
                                         "--timeout", "0.5"], stderr=self.log)
        self.sockets = []
        self.leases = []
        deadline = time.monotonic() + 5
        while not (self.root / "control.sock").exists():
            if self.process.poll() is not None or time.monotonic() > deadline:
                self.tearDown()
                self.fail("coordinator failed to start")
            time.sleep(0.01)

    def tearDown(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=3)
        for sock in self.sockets:
            sock.close()
        for fd in self.leases:
            os.close(fd)
        self.log.close()
        self.temp.cleanup()

    def connect(self):
        sock = socket.socket(socket.AF_UNIX)
        sock.settimeout(2)
        sock.connect(str(self.root / "control.sock"))
        self.sockets.append(sock)
        return sock

    def message(self, sock, op, client, dev=0, ino=0, size=0):
        sock.sendall(COORD.FRAME.pack(op, client, dev, ino, size))

    def ack(self, sock):
        self.assertEqual(COORD.REPLY.unpack(COORD.receive(sock, COORD.REPLY.size)), (0,))

    def peer(self, identity):
        lease = os.open(self.root / "lifetime.lock", os.O_RDWR)
        fcntl.flock(lease, fcntl.LOCK_SH | fcntl.LOCK_NB)
        self.leases.append(lease)
        sock = self.connect()
        self.message(sock, COORD.REGISTER, identity)
        self.ack(sock)
        return sock

    def transaction(self, identity):
        sock = self.connect()
        self.message(sock, COORD.BEGIN, identity)
        self.ack(sock)
        return sock

    def test_resize_waits_for_every_participant(self):
        peers = [self.peer(1), self.peer(2)]
        tx = self.transaction(1)
        self.message(tx, COORD.RESIZE, 1, 11, 22, 17)
        for peer in peers:
            self.assertEqual(COORD.FRAME.unpack(COORD.receive(peer, COORD.FRAME.size)),
                             (COORD.RESIZE, 0, 11, 22, 17))
            tx.settimeout(0.03)
            with self.assertRaises(TimeoutError):
                tx.recv(8)
            peer.sendall(COORD.REPLY.pack(0))
        tx.settimeout(2)
        self.ack(tx)
        self.message(tx, COORD.END, 1)
        self.ack(tx)

    def test_stopped_participant_poison_prevents_success_reply(self):
        peer = self.peer(1)
        tx = self.transaction(1)
        self.message(tx, COORD.RESIZE, 1, 11, 22, 0)
        COORD.receive(peer, COORD.FRAME.size)
        self.assertEqual(self.process.wait(timeout=3), 78)
        self.assertEqual(tx.recv(8), b"")
        self.assertIn('"event": "poison"', (self.root / "events.jsonl").read_text())
        challenger = os.open(self.root / "lifetime.lock", os.O_RDWR)
        try:
            with self.assertRaises(BlockingIOError):
                fcntl.flock(challenger, fcntl.LOCK_EX | fcntl.LOCK_NB)
        finally:
            os.close(challenger)

    def test_second_transaction_cannot_enter_until_first_completes(self):
        self.peer(1)
        self.peer(2)
        first = self.transaction(1)
        second = self.connect()
        self.message(second, COORD.BEGIN, 2)
        second.settimeout(0.03)
        with self.assertRaises(TimeoutError):
            second.recv(8)
        self.message(first, COORD.END, 1)
        self.ack(first)
        second.settimeout(2)
        self.ack(second)
        self.message(second, COORD.END, 2)
        self.ack(second)


if __name__ == "__main__":
    unittest.main()
