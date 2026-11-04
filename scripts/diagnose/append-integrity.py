#!/usr/bin/env python3
"""Check append integrity in disposable files under each supplied directory.

Run inside the guest/container to exercise its filesystem, e.g.:
    python3 scripts/diagnose/append-integrity.py /tmp "$PWD" /home/sandbox/.codex

Uses one process and sequential operations, not the concurrent truncate/read
workload known to wedge 9p. Never opens an existing file for writing. A mismatch
returns exit status 1; an OS error returns 2. Files are removed after each probe.
"""

import argparse
import json
import os
from pathlib import Path
import tempfile


def probe(root, name, flags, seek):
    with tempfile.TemporaryDirectory(prefix=".append-integrity-", dir=root) as tmp:
        path = Path(tmp) / "records.jsonl"
        expected = bytearray()
        first_failure = None
        fd = os.open(path, os.O_CREAT | os.O_EXCL | flags, 0o600)
        try:
            for i in range(12):
                packet = (json.dumps({"n": i, "data": "x" * (233 + i * 37)}) + "\n").encode()
                if seek:
                    os.lseek(fd, 0, os.SEEK_END)
                written = os.write(fd, packet)
                if written != len(packet):
                    raise OSError(f"short write: {written}/{len(packet)} bytes")
                expected.extend(packet)
                # Force page-cache/server interaction between append calls.
                os.fsync(fd)
                os.fstat(fd)
                actual = path.read_bytes()
                if actual != expected and first_failure is None:
                    first_failure = {
                        "write": i + 1,
                        "expected_bytes": len(expected),
                        "actual_bytes": len(actual),
                        "nul_bytes": actual.count(b"\0"),
                    }
        finally:
            os.close(fd)
        actual = path.read_bytes()
        matches = actual == expected and first_failure is None
        result = {
            "directory": str(root),
            "mode": name,
            "pass": matches,
            "expected_bytes": len(expected),
            "actual_bytes": len(actual),
            "nul_bytes": actual.count(b"\0"),
            "first_failure": first_failure,
        }
        print(json.dumps(result), flush=True)
        return matches


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directories", nargs="+", type=Path)
    args = parser.parse_args()
    modes = [
        ("rdwr-append", os.O_RDWR | os.O_APPEND, False),
        ("wronly-append", os.O_WRONLY | os.O_APPEND, False),
        ("rdwr-append-sync", os.O_RDWR | os.O_APPEND | os.O_SYNC, False),
        # A diagnostic control only: seek+write is not atomic append and is
        # not a safe replacement when other processes can write the file.
        ("rdwr-seek-end", os.O_RDWR, True),
    ]
    status = 0
    for root in args.directories:
        for name, flags, seek in modes:
            try:
                if not probe(root, name, flags, seek):
                    status = max(status, 1)
            except OSError as error:
                print(json.dumps({"directory": str(root), "mode": name, "error": str(error)}), flush=True)
                status = 2
    return status


if __name__ == "__main__":
    raise SystemExit(main())
