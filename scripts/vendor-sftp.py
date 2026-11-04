#!/usr/bin/env python3
"""Reproduce the pinned SFTP server and its bounded-decoding and cleanup fixes."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
DEST = ROOT / "third_party/sftp-v2"
VERSION = "v2.0.0-alpha2"
MODULE = "github.com/pkg/sftp/v2"
SUM = "h1:BNLMebIYmcTEqlM30TFEaIc3ICpeaPVCeDniARthgo4="


def files(root):
    return {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="Compare every file without modifying the repository")
    args = parser.parse_args()
    module = json.loads(subprocess.check_output(
        ["go", "mod", "download", "-json", f"{MODULE}@{VERSION}"], cwd=ROOT,
    ))
    if module.get("Sum") != SUM:
        parser.error("upstream module checksum mismatch")
    source = Path(module["Dir"])
    with tempfile.TemporaryDirectory(prefix="yoloctl-sftp-") as temp:
        work = Path(temp)
        for path in source.rglob("*"):
            if not path.is_file():
                continue
            if (path.suffix == ".go" and not path.name.endswith("_test.go")) or path.name in ("go.mod", "go.sum", "LICENSE"):
                target = work / path.relative_to(source)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, target)
        for patch in ("sftp-v2-count-bounds.patch", "sftp-v2-cleanup.patch"):
            subprocess.run(["patch", "-t", "-p1", "-i", str(ROOT / "third_party" / patch)], cwd=work, check=True)
        expected = files(work)
        if args.check:
            if files(DEST) != expected:
                parser.error("local SFTP server differs from the pinned source plus patch")
            print("SFTP server reproduced byte for byte")
        else:
            if DEST.exists():
                shutil.rmtree(DEST)
            shutil.copytree(work, DEST)
            print(f"SFTP server: {MODULE}@{VERSION} plus count bounds and server cleanup")


if __name__ == "__main__":
    main()
