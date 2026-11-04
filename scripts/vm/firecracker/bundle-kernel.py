#!/usr/bin/env python3
"""Maintainer-only update of the kernel component embedded by Go builds."""
import argparse
import gzip
import importlib.util
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
DEST = REPO / "assets/vm/firecracker"
spec = importlib.util.spec_from_file_location("kernel_inputs", DEST / "inputs.py")
inputs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inputs)


def bundle(source):
    source = Path(source)
    manifest = json.loads((source / "kernel.json").read_text())
    if (manifest.get("inputs") != {name: inputs.PINS[name] for name in sorted(inputs.KERNEL_INPUTS)}
            or manifest.get("enabled") != inputs.KERNEL_EXTRAS
            or manifest.get("disabled") != inputs.KERNEL_DISABLED
            or manifest.get("kernel_sha256") != inputs.digest(source / "kernel")
            or manifest.get("config_sha256") != inputs.digest(source / "kernel.config")):
        raise RuntimeError("kernel does not match its pinned build recipe")
    config = (source / "kernel.config").read_text().splitlines()
    if any(f"CONFIG_{name}=y" not in config for name in inputs.KERNEL_EXTRAS):
        raise RuntimeError("kernel lacks required features")
    if any(f"# CONFIG_{name} is not set" not in config for name in inputs.KERNEL_DISABLED):
        raise RuntimeError("kernel enables unsupported features")
    for name in ["kernel.json", "kernel.config"]:
        (DEST / name).write_bytes((source / name).read_bytes())
    (DEST / "kernel-linux-amd64.gz").write_bytes(gzip.compress((source / "kernel").read_bytes(), mtime=0))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kernel-build", required=True)
    bundle(parser.parse_args().kernel_build)
