#!/usr/bin/env python3
"""Reproduce the pinned host renderer and its color, mouse, text-input and split-terminator fixes."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
DEST = ROOT / "third_party/ultraviolet"
VERSION = "v0.0.0-20261001125412-878653296cfd"
MODULE = "github.com/charmbracelet/ultraviolet"
SUM = "h1:W0HOPGkwRaAmV20plE/f+Rk6Xn+IdR8G0qHsGXcOnF8="


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
    with tempfile.TemporaryDirectory(prefix="yoloctl-ultraviolet-") as temp:
        work = Path(temp)
        for path in source.rglob("*"):
            if not path.is_file():
                continue
            if (path.suffix == ".go" and not path.name.endswith("_test.go")) or path.name in ("go.mod", "go.sum", "LICENSE"):
                target = work / path.relative_to(source)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(path, target)
        subprocess.run(["patch", "-t", "-p1", "-i", str(ROOT / "third_party/ultraviolet.patch")], cwd=work, check=True)
        expected = files(work)
        if args.check:
            if files(DEST) != expected:
                parser.error("local renderer differs from the pinned source plus patch")
            print("Host renderer reproduced byte for byte")
        else:
            if DEST.exists():
                shutil.rmtree(DEST)
            shutil.copytree(work, DEST)
            print(f"Host renderer: {MODULE}@{VERSION} plus palette-identity, signed-mouse, text-input and split-terminator fixes")


if __name__ == "__main__":
    main()
