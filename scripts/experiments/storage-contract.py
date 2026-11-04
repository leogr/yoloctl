#!/usr/bin/env python3
"""Bounded, synthetic storage checks across two pre-existing execution contexts.

This does not create containers/VMs or certify a backend. Use dedicated empty
directories in disposable environments. 9p is refused before any test writes.
Control messages travel over stdio, never through files under test.
"""

import argparse
import datetime as dt
import errno
import fcntl
import hashlib
import itertools
import json
import mmap
import os
from pathlib import Path
import re
import selectors
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid

PREFIX = b"YOLOCTL_STORAGE_PROBE "
TIMEOUT = 20


def filename(name):
    if not re.fullmatch(r"[a-z][a-z0-9_.-]{0,80}", name):
        raise ValueError("invalid synthetic filename")
    return name


def filesystem(path):
    matches = []
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        before, after = line.split(" - ", 1)
        mount = re.sub(r"\\([0-7]{3})", lambda m: chr(int(m[1], 8)), before.split()[4])
        if path == mount or path.startswith(mount.rstrip("/") + "/"):
            matches.append((len(mount), after.split()[0]))
    if not matches:
        raise RuntimeError("cannot identify backing filesystem")
    return max(matches)[1]


class Worker:
    def __init__(self, root, run_id):
        self.root = Path(root).resolve(strict=True)
        self.run_id = run_id
        if not re.fullmatch(r"storage-contract-[0-9a-f]{32}", run_id):
            raise ValueError("invalid run ID")
        self.rootfd = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        self.runfd = None
        self.handles = {}
        self.mapping = None
        self.db = None
        try:
            self.fstype = filesystem(str(self.root))
            if self.fstype == "9p":
                raise RuntimeError("9p refused; do not probe the enclosing development VM")
            if os.listdir(self.rootfd):
                raise RuntimeError("probe root must be an empty, dedicated directory")
        except Exception:
            os.close(self.rootfd)
            raise

    def close(self):
        if self.mapping is not None:
            self.mapping.close()
            self.mapping = None
        if self.db is not None:
            self.db.close()
            self.db = None
        for descriptor in self.handles.values():
            os.close(descriptor)
        self.handles.clear()

    def dispatch(self, command):
        op = command["op"]
        if op == "info":
            return {"kernel": os.uname().release, "filesystem": self.fstype,
                    "sqlite_version": sqlite3.sqlite_version,
                    "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip(),
                    "pid_namespace": os.readlink("/proc/self/ns/pid"),
                    "uid": os.getuid(), "root": str(self.root)}
        if op == "prepare":
            if command["create"]:
                os.mkdir(self.run_id, mode=0o700, dir_fd=self.rootfd)
            self.runfd = os.open(self.run_id, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW,
                                 dir_fd=self.rootfd)
            return True
        if self.runfd is None:
            raise RuntimeError("worker not prepared")
        if op == "open":
            name = filename(command["name"])
            flags = os.O_RDWR | os.O_NOFOLLOW
            if command.get("create"):
                flags |= os.O_CREAT | os.O_EXCL
            if command.get("append"):
                flags |= os.O_APPEND
            self.handles[command["handle"]] = os.open(name, flags, 0o600, dir_fd=self.runfd)
            return True
        if op == "lookup":
            try:
                os.stat(filename(command["name"]), dir_fd=self.runfd, follow_symlinks=False)
                return "present"
            except FileNotFoundError:
                return "absent"
        if op == "replace":
            os.replace(filename(command["source"]), filename(command["target"]),
                       src_dir_fd=self.runfd, dst_dir_fd=self.runfd)
            return True
        if op == "duplicate":
            self.handles[command["duplicate"]] = os.dup(self.handles[command["handle"]])
            return True
        if op == "close_handle":
            os.close(self.handles.pop(command["handle"]))
            return True
        if op == "write":
            data = bytes.fromhex(command["hex"])
            count = os.write(self.handles[command["handle"]], data)
            if command.get("sync"):
                os.fsync(self.handles[command["handle"]])
            return count
        if op == "pwrite":
            return os.pwrite(self.handles[command["handle"]], bytes.fromhex(command["hex"]),
                             command["offset"])
        if op == "read":
            data = os.pread(self.handles[command["handle"]], 1024 * 1024, 0)
            if command.get("sort_records"):
                data = b"".join(sorted(data.splitlines(keepends=True)))
            return {"size": len(data), "sha256": hashlib.sha256(data).hexdigest(),
                    "nul_bytes": data.count(b"\0")}
        if op == "position":
            return os.lseek(self.handles[command["handle"]], 0, os.SEEK_CUR)
        if op == "append_batch":
            descriptor = self.handles["concurrent-append"]
            for step in range(128):
                data = append_record(command["owner"], step)
                if os.write(descriptor, data) != len(data):
                    raise RuntimeError("short append write")
                # Keep both writers eligible to run without filesystem barriers.
                time.sleep(0.001)
            os.fsync(descriptor)
            return True
        if op == "lock":
            fd = self.handles[command["handle"]]
            mode = {"exclusive": fcntl.LOCK_EX, "shared": fcntl.LOCK_SH,
                    "unlock": fcntl.LOCK_UN}[command["mode"]]
            if mode != fcntl.LOCK_UN:
                mode |= fcntl.LOCK_NB
            try:
                if command["kind"] == "flock":
                    fcntl.flock(fd, mode)
                else:
                    fcntl.lockf(fd, mode, 1, command.get("offset", 0), os.SEEK_SET)
                return "acquired" if mode != fcntl.LOCK_UN else "released"
            except OSError as exc:
                if exc.errno in (errno.EAGAIN, errno.EACCES):
                    return "contended"
                raise
        if op == "size":
            os.ftruncate(self.handles[command["handle"]], command["size"])
            return True
        if op == "map":
            self.mapping = mmap.mmap(self.handles["mapping"], 8192, access=mmap.ACCESS_WRITE)
            return self.mapping[0:8].hex()
        if op == "map_write":
            data = bytes.fromhex(command["hex"])
            offset = command["offset"]
            self.mapping[offset:offset + len(data)] = data
            # Deliberately no flush, msync, fsync, reopen, or remap.
            return True
        if op == "map_read":
            offset = command["offset"]
            return self.mapping[offset:offset + 8].hex()
        if op == "wal_open":
            self.db = sqlite3.connect(str(self.root / self.run_id / "wal.sqlite"),
                                      timeout=0, isolation_level=None)
            mode = self.db.execute("PRAGMA journal_mode=WAL").fetchone()[0]
            self.db.execute("PRAGMA synchronous=FULL")
            if command.get("create"):
                self.db.execute("CREATE TABLE counter (id INTEGER PRIMARY KEY, value INTEGER)")
                self.db.execute("INSERT INTO counter VALUES (1, 0)")
                self.db.execute("CREATE TABLE events (owner TEXT, step INTEGER, PRIMARY KEY(owner, step))")
            return {"journal_mode": mode, "sqlite_version": sqlite3.sqlite_version}
        if op == "wal_begin":
            try:
                self.db.execute("BEGIN IMMEDIATE" if command.get("write") else "BEGIN")
                return "begun"
            except sqlite3.OperationalError as exc:
                if getattr(exc, "sqlite_errorcode", None) == getattr(sqlite3, "SQLITE_BUSY", 5) or str(exc) == "database is locked":
                    return "contended"
                raise
        if op == "wal_value":
            return self.db.execute("SELECT value FROM counter WHERE id=1").fetchone()[0]
        if op == "wal_increment":
            self.db.execute("UPDATE counter SET value=value+1 WHERE id=1")
            return True
        if op == "wal_commit":
            self.db.execute("COMMIT")
            return True
        if op == "wal_batch":
            self.db.execute("PRAGMA busy_timeout=5000")
            for step in range(64):
                self.db.execute("BEGIN IMMEDIATE")
                self.db.execute("UPDATE counter SET value=value+1 WHERE id=1")
                self.db.execute("INSERT INTO events VALUES (?, ?)", (command["owner"], step))
                self.db.execute("COMMIT")
            return True
        if op == "wal_check":
            return {"integrity": self.db.execute("PRAGMA integrity_check").fetchall(),
                    "value": self.db.execute("SELECT value FROM counter WHERE id=1").fetchone()[0],
                    "events": self.db.execute("SELECT owner, count(*) FROM events GROUP BY owner ORDER BY owner").fetchall()}
        if op == "close":
            self.close()
            return True
        if op == "cleanup":
            self.close()
            for name in os.listdir(self.runfd):
                if name not in {"witness", "append", "append-position", "concurrent-append", "posix", "flock", "mapping",
                                "eof", "rename-current", "rename-next", "rename-published",
                                "wal.sqlite", "wal.sqlite-wal", "wal.sqlite-shm"}:
                    raise RuntimeError("unexpected file in synthetic directory; retained")
                os.unlink(name, dir_fd=self.runfd)
            os.rmdir(self.run_id, dir_fd=self.rootfd)
            return True
        raise ValueError("unknown operation")


def reply(message):
    sys.stdout.buffer.write(PREFIX + json.dumps(message).encode() + b"\n")
    sys.stdout.buffer.flush()


def worker_main(root, run_id):
    try:
        worker = Worker(root, run_id)
    except Exception as exc:
        reply({"error": str(exc)})
        return 1
    try:
        for line in sys.stdin:
            command = json.loads(line)
            if command["op"] == "quit":
                break
            try:
                reply({"result": worker.dispatch(command)})
            except Exception as exc:
                reply({"error": str(exc), "type": type(exc).__name__})
    finally:
        worker.close()
        if worker.runfd is not None:
            os.close(worker.runfd)
        os.close(worker.rootfd)
    return 0


class Endpoint:
    def __init__(self, config, code, run_id):
        self.name = config["name"]
        self.buffer = b""
        self.stderr = tempfile.TemporaryFile()
        argv = config.get("prefix", []) + [config.get("python", "python3"), "-u", "-c",
                                           code, "--worker", config["root"], run_id]
        self.process = subprocess.Popen(argv, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                        stderr=self.stderr, start_new_session=True)
        self.selector = selectors.DefaultSelector()
        self.selector.register(self.process.stdout, selectors.EVENT_READ)

    def send(self, op, **arguments):
        self.process.stdin.write(json.dumps({"op": op, **arguments}).encode() + b"\n")
        self.process.stdin.flush()

    def receive(self):
        deadline = time.monotonic() + TIMEOUT
        while time.monotonic() < deadline:
            while b"\n" in self.buffer:
                line, self.buffer = self.buffer.split(b"\n", 1)
                if not line.startswith(PREFIX):
                    continue
                message = json.loads(line[len(PREFIX):])
                if "error" in message:
                    raise RuntimeError(f"{self.name}: {message['error']}")
                return message["result"]
            if not self.selector.select(min(1, max(0, deadline - time.monotonic()))):
                continue
            chunk = os.read(self.process.stdout.fileno(), 65536)
            if not chunk:
                raise RuntimeError(f"{self.name}: worker exited without a response")
            self.buffer += chunk
            if len(self.buffer) > 1024 * 1024:
                raise RuntimeError(f"{self.name}: excessive worker output")
        raise TimeoutError(f"{self.name}: worker deadline exceeded; this alone is not corruption evidence")

    def call(self, op, **arguments):
        self.send(op, **arguments)
        return self.receive()

    def stop(self):
        if self.process.poll() is None:
            try:
                self.send("quit")
                self.process.wait(timeout=2)
            except (OSError, subprocess.TimeoutExpired):
                self.signal(signal.SIGTERM)
                try:
                    self.process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    self.signal(signal.SIGKILL)
                    self.process.wait(timeout=2)
        self.selector.close()
        self.process.stdin.close()
        self.process.stdout.close()
        self.stderr.close()

    def signal(self, signum):
        try:
            os.killpg(self.process.pid, signum)
        except ProcessLookupError:
            pass


def expect(actual, expected, label):
    if actual != expected:
        raise AssertionError(f"{label}: expected {expected!r}, got {actual!r}")


def digest(data):
    return {"size": len(data), "sha256": hashlib.sha256(data).hexdigest(),
            "nul_bytes": data.count(b"\0")}


def append_record(owner, step):
    return (json.dumps({"owner": owner, "step": step,
                        "payload": "x" * (251 + step % 97)}) + "\n").encode()


def check_persistent_eof(a, b, record):
    for peer in (a, b):
        peer.call("open", name="eof", handle="eof", create=peer is a)
        expect(peer.call("read", handle="eof"), digest(b""), "prime empty descriptor")
    # No mappings on this file, no racing truncate/read loop, and no reader
    # stat/open/seek before pread. Each write finishes before the peer reads.
    for step, size in enumerate((17, 8209, 31, 0, 4097, 12288, 1, 0)):
        writer, reader = (a, b) if step % 2 == 0 else (b, a)
        expected = bytes([65 + step]) * size
        writer.call("size", handle="eof", size=size)
        if expected:
            expect(writer.call("pwrite", handle="eof", offset=0, hex=expected.hex()),
                   size, "write resized file")
        expect(reader.call("read", handle="eof"), digest(expected),
               "persistent descriptor observes peer growth/shrink without metadata refresh")
    record.append("persistent descriptors observe peer growth/shrink without stat/reopen")


def check_namespace(a, b, record):
    a.call("open", name="rename-current", handle="rename-old", create=True)
    old = b"old inode retained by open descriptions"
    new = b"new inode published by rename"
    a.call("write", handle="rename-old", hex=old.hex())
    b.call("open", name="rename-current", handle="rename-old")
    expect(b.call("read", handle="rename-old"), digest(old), "prime old inode")
    expect(b.call("lookup", name="rename-published"), "absent", "prime negative lookup")
    a.call("open", name="rename-next", handle="rename-new", create=True)
    a.call("write", handle="rename-new", hex=new.hex())
    a.call("replace", source="rename-next", target="rename-current")
    for peer in (a, b):
        expect(peer.call("read", handle="rename-old"), digest(old),
               "rename preserves the inode behind existing descriptors")
        peer.call("open", name="rename-current", handle="rename-fresh")
        expect(peer.call("read", handle="rename-fresh"), digest(new),
               "fresh open observes replacement inode")
    # The second rename also tests a previously cached negative lookup.
    a.call("replace", source="rename-current", target="rename-published")
    expect(b.call("lookup", name="rename-current"), "absent", "renamed path disappears")
    b.call("open", name="rename-published", handle="rename-published")
    expect(b.call("read", handle="rename-published"), digest(new),
           "fresh open observes creation after a negative lookup")
    expect(b.call("read", handle="rename-fresh"), digest(new), "second rename retains open inode")
    record.append("rename preserves open inode identity and refreshes positive/negative lookups")


def check_mixed_io(a, b, expected, record):
    for peer in (a, b):
        expect(peer.call("read", handle="mapping"), digest(expected), "prime file I/O view")
    for step, (writer, reader) in enumerate(((a, b), (b, a), (a, b), (b, a))):
        offset = 128 + (step % 2) * 4096
        value = (100 + step).to_bytes(8, "little")
        writer.call("map_write", offset=offset, hex=value.hex())
        expected[offset:offset + 8] = value
        expect(reader.call("read", handle="mapping"), digest(expected),
               "file read observes peer mmap store without flush")
        value = (200 + step).to_bytes(8, "little")
        expect(writer.call("pwrite", handle="mapping", offset=offset, hex=value.hex()),
               len(value), "positioned file write")
        expected[offset:offset + 8] = value
        expect(reader.call("map_read", offset=offset), value.hex(),
               "persistent mapping observes peer file write without remap")
    record.append("bidirectional mmap/file I/O coherence without flush/reopen/remap")


def check_contract(a, b, record):
    a.call("prepare", create=True)
    b.call("prepare", create=False)
    a.call("open", name="witness", handle="witness", create=True)
    witness = uuid.uuid4().hex.encode()
    a.call("write", handle="witness", hex=witness.hex(), sync=True)
    b.call("open", name="witness", handle="witness")
    expect(b.call("read", handle="witness"), digest(witness), "same-directory witness")
    record.append("same-directory witness")

    a.call("open", name="append", handle="append", create=True, append=True)
    b.call("open", name="append", handle="append")
    expected = b""
    for index in range(12):
        data = (json.dumps({"step": index, "payload": "x" * (251 + 37 * index)}) + "\n").encode()
        expect(a.call("write", handle="append", hex=data.hex(), sync=True), len(data), "append count")
        expected += data
        for peer in (a, b):
            expect(peer.call("read", handle="append"), digest(expected), f"append bytes via {peer.name}")
    record.append("read/write append with persistent writer and intermediate readers")

    # Both descriptions predate every write. Appending at the correct host
    # offset must also advance the *guest* description to that actual end.
    # No peer stat/read refreshes its cached size before its next append.
    for peer in (a, b):
        peer.call("open", name="append-position", handle="append-position",
                  create=peer is a, append=True)
    expected = b""
    for step, writer in enumerate(itertools.islice(itertools.cycle((a, b)), 6)):
        data = append_record(writer.name, step)
        expect(writer.call("write", handle="append-position", hex=data.hex()),
               len(data), "alternating append count")
        expected += data
        expect(writer.call("position", handle="append-position"), len(expected),
               "append descriptor advances to the actual end after a peer write")
    for peer in (a, b):
        expect(peer.call("read", handle="append-position"), digest(expected),
               "alternating append contents")
    record.append("alternating appends update each persistent descriptor's actual offset")

    for peer in (a, b):
        peer.call("open", name="concurrent-append", handle="concurrent-append",
                  create=peer is a, append=True)
    for peer in (a, b):
        peer.send("append_batch", owner=peer.name)
    for peer in (a, b):
        expect(peer.receive(), True, "concurrent append batch")
    records = [append_record(peer.name, step) for peer in (a, b) for step in range(128)]
    expected_records = digest(b"".join(sorted(records)))
    for peer in (a, b):
        expect(peer.call("read", handle="concurrent-append", sort_records=True),
               expected_records, f"complete concurrent append records via {peer.name}")
    record.append("two append writers retain 256 exact records without advisory locks")

    check_persistent_eof(a, b, record)
    check_namespace(a, b, record)

    for kind in ("posix", "flock"):
        for peer in (a, b):
            peer.call("open", name=kind, handle=kind, create=peer is a)
        def lock(peer, mode, offset=0):
            return peer.call("lock", handle=kind, kind=kind, mode=mode, offset=offset)
        for holder, contender in ((a, b), (b, a)):
            expect(lock(holder, "exclusive"), "acquired", f"{kind} acquire")
            expect(lock(contender, "exclusive"), "contended", f"{kind} excludes {contender.name}")
            lock(holder, "unlock")
            expect(lock(contender, "shared"), "acquired", f"{kind} release")
            expect(lock(holder, "shared"), "acquired", f"{kind} shared readers")
            lock(holder, "unlock")
            expect(lock(holder, "exclusive"), "contended", f"{kind} shared excludes writer")
            lock(contender, "unlock")
        if kind == "posix":
            expect(lock(a, "exclusive", 0), "acquired", "first byte lock")
            expect(lock(b, "exclusive", 1), "acquired", "independent byte lock")
            lock(a, "unlock", 0)
            lock(b, "unlock", 1)
        record.append(f"{kind} nonblocking locks in both directions")

        # POSIX locks belong to the process and close on *any* descriptor
        # for the inode. Flock follows the open description through dup.
        for holder, contender in ((a, b), (b, a)):
            expect(lock(holder, "exclusive"), "acquired", f"{kind} owner acquires")
            holder.call("open", name=kind, handle="independent")
            holder.call("close_handle", handle="independent")
            if kind == "posix":
                expect(lock(contender, "exclusive"), "acquired",
                       "closing independent descriptor releases process-owned POSIX locks")
                lock(contender, "unlock")
                expect(lock(holder, "exclusive"), "acquired", "POSIX owner reacquires")
            else:
                expect(lock(contender, "exclusive"), "contended",
                       "closing independent descriptor retains flock")

            holder.call("duplicate", handle=kind, duplicate="duplicate")
            holder.call("close_handle", handle=kind)
            if kind == "posix":
                expect(lock(contender, "exclusive"), "acquired",
                       "POSIX close releases locks despite surviving duplicate")
                lock(contender, "unlock")
            else:
                expect(lock(contender, "exclusive"), "contended",
                       "flock survives through duplicate")
            holder.call("close_handle", handle="duplicate")
            expect(lock(contender, "exclusive"), "acquired", f"{kind} final close releases")
            lock(contender, "unlock")
            holder.call("open", name=kind, handle=kind)
        record.append(f"{kind} independent-open and duplicate-descriptor close ownership")

    a.call("open", name="mapping", handle="mapping", create=True)
    a.call("size", handle="mapping", size=8192)
    b.call("open", name="mapping", handle="mapping")
    for peer in (a, b):
        expect(peer.call("map"), bytes(8).hex(), "initial shared mapping")
        peer.call("map_read", offset=4096)
    expected_mapping = bytearray(8192)
    for step, (writer, reader) in enumerate(itertools.islice(itertools.cycle(((a, b), (b, a))), 16)):
        offset = (step % 2) * 4096
        value = (step + 1).to_bytes(8, "little").hex()
        writer.call("map_write", offset=offset, hex=value)
        expected_mapping[offset:offset + 8] = bytes.fromhex(value)
        expect(reader.call("map_read", offset=offset), value, "persistent mmap visibility without flush")
    record.append("bidirectional persistent shared mmap without flush/reopen/remap")
    check_mixed_io(a, b, expected_mapping, record)

    for peer in (a, b):
        result = peer.call("wal_open", create=peer is a)
        expect(result["journal_mode"], "wal", "actual WAL mode")
    expect(a.call("wal_begin", write=True), "begun", "first WAL writer")
    expect(b.call("wal_begin", write=True), "contended", "WAL excludes second writer")
    a.call("wal_commit")
    expect(a.call("wal_begin"), "begun", "reader begins")
    expect(a.call("wal_value"), 0, "initial reader snapshot")
    expect(b.call("wal_begin", write=True), "begun", "writer alongside reader")
    b.call("wal_increment")
    b.call("wal_commit")
    expect(a.call("wal_value"), 0, "reader snapshot survives external commit")
    a.call("wal_commit")
    expect(a.call("wal_value"), 1, "new snapshot sees commit")
    for peer in (a, b):
        peer.send("wal_batch", owner=peer.name)
    for peer in (a, b):
        expect(peer.receive(), True, "overlapping WAL batch")
    expected_check = {"integrity": [["ok"]], "value": 129,
                      "events": sorted([[a.name, 64], [b.name, 64]])}
    for peer in (a, b):
        expect(peer.call("wal_check"), expected_check, f"committed WAL result via {peer.name}")
    record.append("WAL writer exclusion, reader snapshot, 128 overlapping transactions and integrity")


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "--worker":
        return worker_main(sys.argv[2], sys.argv[3])
    parser = argparse.ArgumentParser(description=__doc__)
    choice = parser.add_mutually_exclusive_group(required=True)
    choice.add_argument("--local-control", action="store_true",
                        help="two local processes on an empty /tmp directory; no isolation proof")
    choice.add_argument("--endpoints", type=Path,
                        help="JSON list of exactly two {name,root,prefix?,python?} execution contexts")
    parser.add_argument("--report", type=Path)
    parser.add_argument("--keep", action="store_true", help="retain synthetic data even on success")
    args = parser.parse_args()
    if args.report and (args.report.exists() or args.report.is_symlink()):
        parser.error("report already exists; choose a new path")
    local_root = None
    if args.local_control:
        local_root = tempfile.mkdtemp(prefix="yoloctl-storage-control-", dir="/tmp")
        configs = [{"name": name, "root": local_root} for name in ("a", "b")]
    else:
        configs = json.loads(args.endpoints.read_text())
    if not isinstance(configs, list) or len(configs) != 2:
        parser.error("exactly two endpoints are required")
    for entry in configs:
        if not isinstance(entry, dict) or not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", entry.get("name", "")):
            parser.error("endpoint names must be short lowercase identifiers")
        if not isinstance(entry.get("root"), str) or not entry["root"].startswith("/"):
            parser.error("each root must be an absolute path to an empty dedicated directory")
        if not isinstance(entry.get("prefix", []), list) or not all(isinstance(arg, str) for arg in entry.get("prefix", [])):
            parser.error("prefix must be an argv array, not shell text")
        if not isinstance(entry.get("python", "python3"), str):
            parser.error("python must be an executable name/path")
    if configs[0]["name"] == configs[1]["name"]:
        parser.error("endpoint names must differ")
    run_id = "storage-contract-" + uuid.uuid4().hex
    report = {"started_at": dt.datetime.now(dt.timezone.utc).isoformat(), "run_id": run_id,
              "scope": "local-control" if args.local_control else "specified-endpoints",
              "status": "failed", "endpoints": {}, "passed": [],
              "limits": "No blocking/cancellation/crash/mapped-EOF/durability/isolation or agent-resume certification."}
    peers = []
    try:
        code = Path(__file__).read_text()
        report["probe_sha256"] = hashlib.sha256(code.encode()).hexdigest()
        for entry in configs:
            peer = Endpoint(entry, code, run_id)
            peers.append(peer)
            report["endpoints"][peer.name] = peer.call("info")
        check_contract(peers[0], peers[1], report["passed"])
        for peer in peers:
            peer.call("close")
        if not args.keep:
            peers[0].call("cleanup")
        report["status"] = "passed"
    except Exception as exc:
        report["error"] = f"{type(exc).__name__}: {exc}"
        report["synthetic_data_retained"] = True
    finally:
        for peer in peers:
            try:
                peer.stop()
            except Exception as exc:
                report.setdefault("cleanup_errors", []).append(f"{peer.name}: {type(exc).__name__}")
                report["status"] = "failed"
        if local_root and report["status"] == "passed" and not args.keep:
            os.rmdir(local_root)
    output = json.dumps(report, indent=2) + "\n"
    if args.report:
        # Exclusive creation prevents overwriting an existing research result.
        with args.report.open("x") as stream:
            stream.write(output)
    print(output, end="")
    return 0 if report["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
