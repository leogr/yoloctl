#!/usr/bin/env python3
"""Native Linux control for restart exclusion while old mapping holders survive.

No VM, mount, account, or filesystem service is created. The negative control
demonstrates why a lock held only by the coordinator is insufficient. The
positive control retains independent shared locks in trusted mapping holders.
"""

import argparse
import ctypes
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import mmap
import os
from pathlib import Path
import signal
import socket
import sys
import tempfile


def require(value, message):
    if not value:
        raise RuntimeError(message)


def send(channel, value):
    channel.sendall(json.dumps(value).encode())


def receive(channel):
    value = json.loads(channel.recv(4096))
    if isinstance(value, dict) and "error" in value:
        raise RuntimeError(value["error"])
    return value


class Child:
    def __init__(self, root, role, retain, existing):
        parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        parent.settimeout(2)
        child.settimeout(None)
        controller_pid = os.getpid()
        pid = os.fork()
        if not pid:
            try:
                # A killed controller must not strand the synthetic holders.
                # They are siblings of the tested coordinator, so killing the
                # coordinator itself still leaves them alive for the control.
                libc = ctypes.CDLL(None, use_errno=True)
                require(libc.prctl(1, signal.SIGKILL, 0, 0, 0) == 0,
                        "cannot install parent-death cleanup")
                if os.getppid() != controller_pid:
                    os.kill(os.getpid(), signal.SIGKILL)
                parent.close()
                for sibling in existing:
                    sibling.channel.close()
                self.worker(root, role, retain, child)
                os._exit(0)
            except BaseException as exc:
                try:
                    send(child, {"error": f"{type(exc).__name__}: {exc}"})
                finally:
                    os._exit(1)
        child.close()
        self.pid, self.channel, self.reaped = pid, parent, False

    @staticmethod
    def worker(root, role, retain, channel):
        # Open after fork: these locks have independent open descriptions.
        # The parent and sibling processes never receive these descriptors.
        lock = None
        if role == "coordinator" or retain:
            lock = os.open(root / "lifetime.lock", os.O_RDWR | os.O_NOFOLLOW)
            if role == "coordinator":
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
        mapping = None
        if role != "coordinator":
            fd = os.open(root / "data", os.O_RDWR | os.O_NOFOLLOW)
            mapping = mmap.mmap(fd, 4096, access=mmap.ACCESS_WRITE)
            os.close(fd)
        send(channel, "ready")
        while True:
            command = receive(channel)
            if command["op"] == "write":
                require(mapping is not None, "coordinator cannot write mapped data")
                offset = command["offset"]
                mapping[offset:offset + 4] = command["value"].to_bytes(4, "little")
                send(channel, "written")
            elif command["op"] == "quit":
                # A cooperative holder retires the mapping before its lease.
                if mapping is not None:
                    mapping.close()
                if lock is not None:
                    os.close(lock)
                send(channel, "closed")
                return
            else:
                raise ValueError("unknown operation")

    def write(self, offset, value):
        send(self.channel, {"op": "write", "offset": offset, "value": value})
        require(receive(self.channel) == "written", "mapping write failed")

    def stop(self):
        os.kill(self.pid, signal.SIGSTOP)
        pid, status = os.waitpid(self.pid, os.WUNTRACED)
        require(pid == self.pid and os.WIFSTOPPED(status), "worker did not stop")

    def kill_and_reap(self):
        if self.reaped:
            return
        try:
            os.kill(self.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        pid, status = os.waitpid(self.pid, 0)
        self.reaped = True
        require(pid == self.pid and os.WIFSIGNALED(status) and
                os.WTERMSIG(status) == signal.SIGKILL, "unexpected worker exit")

    def close(self):
        try:
            if not self.reaped:
                try:
                    os.kill(self.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                os.waitpid(self.pid, 0)
                self.reaped = True
        finally:
            self.channel.close()


def restart_can_acquire(root):
    fd = os.open(root / "lifetime.lock", os.O_RDWR | os.O_NOFOLLOW)
    try:
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return True
        except BlockingIOError:
            return False
    finally:
        os.close(fd)


def run_case(root, retain):
    root.mkdir(mode=0o700)
    (root / "lifetime.lock").write_bytes(b"")
    (root / "data").write_bytes(bytes(4096))
    original_lock_inode = (root / "lifetime.lock").stat().st_ino
    children = []
    try:
        for role in ("coordinator", "holder-a", "holder-b"):
            child = Child(root, role, retain, children)
            children.append(child)
            require(receive(child.channel) == "ready", "child not ready")
        coordinator, a, b = children
        require(not restart_can_acquire(root), "live coordinator failed to exclude restart")
        a.write(0, 101)
        coordinator.kill_and_reap()
        can_restart = restart_can_acquire(root)
        require(can_restart != retain, "unexpected restart exclusion after coordinator death")
        b.write(4, 202)
        require((root / "data").read_bytes()[:8] ==
                (101).to_bytes(4, "little") + (202).to_bytes(4, "little"),
                "surviving mapping holders stopped writing")
        if retain:
            a.stop()
            require(not restart_can_acquire(root), "SIGSTOP must not retire mapping lifetime")
            a.kill_and_reap()
            require(not restart_can_acquire(root), "remaining holder must exclude restart")
            b.write(4, 303)
            require((root / "data").read_bytes()[4:8] == (303).to_bytes(4, "little"),
                    "last holder lost its mapping")
        else:
            a.kill_and_reap()
        b.kill_and_reap()
        require(restart_can_acquire(root), "restart still excluded after all holders were reaped")
        require((root / "lifetime.lock").stat().st_ino == original_lock_inode,
                "stable lock was replaced")
        return {
            "status": "passed", "independent_holder_locks": retain,
            "restart_allowed_while_old_mappings_live": can_restart,
            "survivors_wrote_after_coordinator_death": True,
            "restart_allowed_after_all_holders_reaped": True,
        }
    finally:
        for child in children:
            child.close()


def deadline(_signal, _frame):
    raise TimeoutError("native control deadline exceeded")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    if sys.platform != "linux":
        parser.error("this native control supports Linux only")
    if args.report and (args.report.exists() or args.report.is_symlink()):
        parser.error("report already exists; choose a new path")
    probe = Path(__file__).with_name("storage-contract.py")
    spec = importlib.util.spec_from_file_location("storage_contract", probe)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    report = {
        "started_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "scope": "native-process-lifetime-control", "kernel": os.uname().release,
        "source_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "status": "failed",
        "limits": "Trusted local holders only; no KVM, service protocol, dynamic admission, "
                  "stale-message test, macOS, malicious VMM, or durability qualification.",
    }
    signal.signal(signal.SIGALRM, deadline)
    signal.alarm(20)
    try:
        with tempfile.TemporaryDirectory(prefix="yoloctl-coordinator-control-", dir="/tmp") as tmp:
            root = Path(tmp)
            report["filesystem"] = module.filesystem(str(root))
            require(report["filesystem"] != "9p", "9p refused before test writes")
            report["coordinator_only_negative_control"] = run_case(root / "negative", False)
            report["holder_lifetime_control"] = run_case(root / "positive", True)
            report["status"] = "passed"
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        signal.alarm(0)
    output = json.dumps(report, indent=2) + "\n"
    if args.report:
        with args.report.open("x") as stream:
            stream.write(output)
    print(output, end="")
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
