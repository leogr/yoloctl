#!/usr/bin/env python3
"""Small exploratory file-I/O measurements; not a correctness certification.

Creates and removes only a fresh private subdirectory. No cache dropping,
existing-data access, VM management, or agent/model execution. Linux only.
"""

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import time
import uuid


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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--label", required=True)
    args = parser.parse_args()
    root = args.root.resolve(strict=True)
    if not root.is_dir():
        parser.error("root must be an existing directory")
    kind = filesystem(str(root))
    if kind == "9p":
        parser.error("9p refused; correctness must precede performance")
    parent = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    directory = "storage-microbench-" + uuid.uuid4().hex
    os.mkdir(directory, mode=0o700, dir_fd=parent)
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent)
    samples = {}
    payload = bytes(range(256)) * 16  # 4 KiB per small file.
    names = [f"file-{i:03d}" for i in range(128)]
    started = time.perf_counter()

    def measure(name, function):
        begin = time.perf_counter_ns()
        result = function()
        samples.setdefault(name, []).append(time.perf_counter_ns() - begin)
        return result

    def create(name):
        target = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=fd)
        try:
            if os.write(target, payload) != len(payload):
                raise RuntimeError("short write")
        finally:
            os.close(target)

    def read(name):
        target = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
        try:
            if os.read(target, len(payload) + 1) != payload:
                raise RuntimeError("small-file contents differ")
        finally:
            os.close(target)

    try:
        for name in names:
            measure("create_write_close_4k", lambda: create(name))
        for _ in range(3):
            for name in names:
                measure("stat_existing", lambda: os.stat(name, dir_fd=fd, follow_symlinks=False))
                measure("open_read_close_4k", lambda: read(name))
        for name in names:
            measure("rename_existing", lambda: os.rename(name, name + ".moved",
                                                         src_dir_fd=fd, dst_dir_fd=fd))
        target = os.open("append", os.O_WRONLY | os.O_CREAT | os.O_EXCL |
                         os.O_APPEND | os.O_NOFOLLOW, 0o600, dir_fd=fd)
        try:
            def append(sync):
                if os.write(target, payload[:256]) != 256:
                    raise RuntimeError("short append")
                if sync:
                    os.fsync(target)
            for _ in range(128):
                measure("append_256b_no_fsync", lambda: append(False))
            os.fsync(target)  # Drain the unsynced batch before measuring sync.
            for _ in range(16):
                measure("append_256b_with_fsync", lambda: append(True))
        finally:
            os.close(target)
    finally:
        # A fresh private leaf and fixed allow-list prevent broad cleanup.
        allowed = set(names) | {name + ".moved" for name in names} | {"append"}
        present = set(os.listdir(fd))
        if present <= allowed:
            for name in present:
                os.unlink(name, dir_fd=fd)
            os.rmdir(directory, dir_fd=parent)
        os.close(fd)
        os.close(parent)
    result = {}
    for name, durations in samples.items():
        ordered = sorted(durations)
        result[name] = {"operations": len(ordered),
                        "median_us": statistics.median(ordered) / 1000,
                        "p95_us": ordered[math.ceil(len(ordered) * 0.95) - 1] / 1000,
                        "total_ms": sum(ordered) / 1_000_000}
    print(json.dumps({"scope": "exploratory-serial-file-microbenchmark",
                      "label": args.label, "filesystem": kind, "root": str(root),
                      "kernel": os.uname().release, "page_size": os.sysconf("SC_PAGESIZE"),
                      "elapsed_seconds": time.perf_counter() - started,
                      "payload_sha256": hashlib.sha256(payload).hexdigest(),
                      "operations": result,
                      "limits": "Hot small working set; includes Python/syscall overhead. "
                      "No cold-cache, agent, contention, cross-VM or comparative-backend claim."}, indent=2))


if __name__ == "__main__":
    main()
