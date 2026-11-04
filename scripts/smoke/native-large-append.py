#!/usr/bin/env python3
"""Synthetic history integrity across process exits and box/VM replacement.

Run only against disposable storage. Expected bytes are regenerated from the
record count, never read from another file on the filesystem under test.
"""
import argparse
import json
import os
from pathlib import Path


def record(index):
    pattern = f"turn-{index}:0123456789abcdefghijklmnopqrstuvwxyz/"
    size = 262144 + 37 * index
    payload = (pattern * (size // len(pattern) + 1))[:size]
    return (json.dumps({"step": index, "payload": payload},
                       separators=(",", ":")) + "\n").encode()


def expected_history(count):
    return b"".join(record(index) for index in range(count))


def verify(path, count):
    expected = expected_history(count)
    actual = path.read_bytes()
    if actual != expected:
        raise RuntimeError(f"history mismatch: expected {count} records / "
                           f"{len(expected)} bytes, got {len(actual)} bytes")
    # Check resumability as JSONL too: NULs, truncation and duplicate turns fail.
    if [json.loads(line)["step"] for line in actual.splitlines()] != list(range(count)):
        raise RuntimeError("history record sequence changed")


def append_batch(path, count, create=False):
    flags = os.O_RDWR | os.O_APPEND
    if create:
        flags |= os.O_CREAT | os.O_EXCL
    fd = os.open(path, flags, 0o600)
    try:
        expected = expected_history(count)
        # Resume verifies before writing, so it cannot mask damaged history.
        if os.pread(fd, len(expected) + 1, 0) != expected:
            raise RuntimeError("history changed before append")
        for index in range(count, count + 8):
            data = record(index)
            if os.write(fd, data) != len(data):
                raise RuntimeError("short history append")
            expected += data
            if os.pread(fd, len(expected) + 1, 0) != expected:
                raise RuntimeError("persistent descriptor mismatch")
            if path.read_bytes() != expected:
                raise RuntimeError("reopened reader mismatch")
        os.fsync(fd)
    finally:
        os.close(fd)
    verify(path, count + 8)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("init", "resume", "verify"))
    parser.add_argument("root", type=Path)
    parser.add_argument("count", type=int)
    args = parser.parse_args()
    if args.count < 0 or (args.action == "init" and args.count != 0):
        parser.error("count must be nonnegative; init requires count 0")
    path = args.root / "synthetic-history.jsonl"
    if args.action == "init":
        args.root.mkdir(mode=0o700)  # refuse to overwrite a previous fixture
        append_batch(path, 0, create=True)
        fd = os.open(args.root, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    else:
        verify(path, args.count)
        if args.action == "resume":
            append_batch(path, args.count)
    print(f"PASS: synthetic history {args.action} (prior records: {args.count})")


if __name__ == "__main__":
    main()
