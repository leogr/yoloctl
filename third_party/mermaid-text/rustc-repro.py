#!/usr/bin/env python3
"""Give this pinned Cargo graph path-independent crate disambiguators.

Cargo's default metadata includes local package identity and flag paths. Source
path remapping alone therefore does not reproduce relocated LTO output. This
wrapper is used only by build-mermaid.py, on its isolated, locked release graph.
The recipe digest separates different patched source graphs; package/version,
crate type, target and cfg distinguish the crates within that graph.
"""

import hashlib
import json
import os
import sys


def normalized_args(args, environ):
    def values(flag):
        return [args[i + 1] for i, arg in enumerate(args[:-1]) if arg == flag]

    if not values("--crate-name"):
        return args
    identity = [
        environ["YOLOCTL_MERMAID_RECIPE_HASH"],
        environ.get("CARGO_PKG_NAME", ""),
        environ.get("CARGO_PKG_VERSION", ""),
        values("--crate-name"), values("--crate-type"),
        values("--target"), sorted(values("--cfg")),
    ]
    metadata = hashlib.sha256(json.dumps(identity, separators=(",", ":")).encode()).hexdigest()[:32]
    args = list(args)
    for i, arg in enumerate(args):
        if arg == "-C" and i + 1 < len(args) and args[i + 1].startswith("metadata="):
            args[i + 1] = "metadata=" + metadata
        elif arg.startswith("-Cmetadata="):
            args[i] = "-Cmetadata=" + metadata
    return args


if __name__ == "__main__":
    compiler, *arguments = sys.argv[1:]
    os.execv(compiler, [compiler, *normalized_args(arguments, os.environ)])
