#!/usr/bin/env python3
"""Exercise the prototype's actual Rust mapping code in two native processes.

This validates neither Linux guest behavior nor KVM. Uses only a fresh /tmp
directory and synthetic bytes; reports are exclusively created and preserved.
"""
import argparse
import datetime
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import sys
import tempfile
import time

HERE = Path(__file__).resolve().parent


class Peer:
    def __init__(self, binary, root, identity, filesystem=False):
        env = os.environ.copy()
        env.update(YCF_ROOT=str(root / "account"), YCF_CONTROL=str(root / "control"),
                   YCF_CLIENT=str(identity), YCF_PARENT=str(os.getpid()))
        self.log = (root / f"worker-{identity}.log").open("xb")
        self.process = subprocess.Popen([str(binary)] + (["--filesystem"] if filesystem else []), env=env, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=self.log)
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.process.stdout, selectors.EVENT_READ)
        self.buffer = b""

    def receive(self):
        deadline = time.monotonic() + 5
        while b"\n" not in self.buffer:
            if time.monotonic() > deadline:
                raise TimeoutError("native peer deadline exceeded")
            if not self.selector.select(0.1):
                continue
            data = os.read(self.process.stdout.fileno(), 4096)
            if not data:
                raise EOFError("native peer exited")
            self.buffer += data
        line, self.buffer = self.buffer.split(b"\n", 1)
        return line.decode()

    def call(self, message):
        self.process.stdin.write((message + "\n").encode())
        self.process.stdin.flush()
        return self.receive()

    def close(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=5)
        self.selector.close()
        self.process.stdin.close()
        self.process.stdout.close()
        self.log.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--filesystem", action="store_true", help="exercise native filesystem backend operations")
    args = parser.parse_args()
    if args.report.exists():
        parser.error("refusing to replace a report")
    root = Path(tempfile.mkdtemp(prefix="ycf-native-", dir="/tmp"))
    spec = importlib.util.spec_from_file_location("contract", HERE.parent / "storage-contract.py")
    contract = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(contract)
    if contract.filesystem(str(root)) == "9p":
        parser.error("9p refused before any test writes")
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "status": "failed", "scope": "native-prototype-components", "artifacts": str(root),
              "host_kernel": os.uname().release, "filesystem": contract.filesystem(str(root)),
              "limits": "No guest kernels, DAX, filesystem operations, durability or macOS qualification."}
    (root / "marker").write_text("ycf-native-control-v1\n")
    for name in ("account", "control"):
        (root / name).mkdir(mode=0o700)
    backing = root / "account/data"
    backing.write_bytes(bytes(12288))
    log = (root / "coordinator.log").open("xb")
    coordinator = subprocess.Popen([sys.executable, str(HERE / "coordinator.py"), str(root / "control"),
                                    "--timeout", "1", "--parent-pid", str(os.getpid())], stdout=log, stderr=log)
    peers = []
    try:
        deadline = time.monotonic() + 5
        while not (root / "control/control.sock").exists():
            if coordinator.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("coordinator did not start")
            time.sleep(0.01)
        if args.filesystem:
            report["scope"] = "native-filesystem-backend-and-wire"
            report["limits"] = "Native FUSE/virtio descriptor control; no guest kernels, DAX, durability, isolation or macOS qualification."
            peer = Peer(args.worker.resolve(), root, 1, filesystem=True)
            peers.append(peer)
            report["checks"] = []
            report["limitations_observed"] = []
            while True:
                line = peer.receive()
                if line == "NATIVE_FS_PASS":
                    break
                if line.startswith("NATIVE_LIMIT "):
                    report["limitations_observed"].append(line.removeprefix("NATIVE_LIMIT "))
                else:
                    report["checks"].append(line)
            assert peer.process.wait(timeout=5) == 0
        else:
            for identity in (1, 2):
                peer = Peer(args.worker.resolve(), root, identity)
                peers.append(peer)
                assert peer.receive() == "READY"
            for cycle in range(64):
                writer, reader = peers[cycle % 2], peers[(cycle + 1) % 2]
                value = 1 + cycle % 250
                assert writer.call(f"write 0 {value}") == "OK"
                assert reader.call("read 0") == str(value)
                assert backing.read_bytes()[0] == value
                assert writer.call("size 17") == "OK"
                assert backing.stat().st_size == 17
                assert reader.call("write 8192 255") == "OK"  # guard only
                assert writer.call("size 12288") == "OK"
                assert reader.call("read 8192") == "0"
                assert backing.read_bytes()[8192] == 0
                assert reader.call("read 0") == str(value)
                assert writer.call("size 0") == "OK"
                assert reader.call("write 0 255") == "OK"
                assert writer.call("size 12288") == "OK"
                assert reader.call("read 0") == "0"
                assert backing.read_bytes()[0] == 0
            report["cycles_passed"] = 64
            first, second = peers
            assert first.call("write 0 11") == "OK"
            assert first.call("write 4096 22") == "OK"
            assert first.call("write 8192 33") == "OK"
            assert first.call("unmap 4096 4096") == "OK"
            assert first.call("read 0") == "11"
            assert first.call("read 8192") == "33"
            # Reuse the hole for an alias of the final file page. Both the
            # retained right segment and new alias must follow resize events.
            assert first.call("map 4096 4096 8192") == "OK"
            assert first.call("write 4096 44") == "OK"
            assert first.call("read 8192") == "44"
            assert second.call("read 8192") == "44"
            assert second.call("read 4096") == "22"
            assert second.call("size 8192") == "OK"
            assert first.call("write 4096 255") == "OK"
            assert first.call("write 8192 254") == "OK"
            assert second.call("size 12288") == "OK"
            assert first.call("read 4096") == "0"
            assert first.call("read 8192") == "0"
            assert first.call("read 0") == "11"
            assert second.call("read 4096") == "22"
            assert first.call("map 0 16384 0") == "OK"
            assert first.call("read 4096") == "22"
            assert backing.read_bytes()[8192] == 0
            report["mapping_intervals"] = "partial removal, split segments, overlapping replacement and aliased guard/regrowth pass"
            stopped = peers[1]
            os.kill(stopped.process.pid, signal.SIGSTOP)
            _, status = os.waitpid(stopped.process.pid, os.WUNTRACED)
            assert os.WIFSTOPPED(status)
            coordinator.kill()
            coordinator.wait(timeout=5)
            assert peers[0].process.wait(timeout=5) == 78
            challenger = os.open(root / "control/lifetime.lock", os.O_RDWR)
            try:
                try:
                    fcntl.flock(challenger, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    pass
                else:
                    raise AssertionError("replacement admitted while stopped holder retains mappings")
                stopped.process.kill()
                stopped.process.wait(timeout=5)
                fcntl.flock(challenger, fcntl.LOCK_EX | fcntl.LOCK_NB)
            finally:
                os.close(challenger)
            report["coordinator_loss"] = "running holder exits; stopped holder excludes replacement until killed/reaped"
        report["status"] = "passed"
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        if coordinator.poll() is None:
            coordinator.kill()
        coordinator.wait(timeout=5)
        for peer in peers:
            peer.close()
        log.close()
    with args.report.open("x") as output:
        json.dump(report, output, indent=2)
        output.write("\n")
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
