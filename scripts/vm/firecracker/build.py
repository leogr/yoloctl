#!/usr/bin/env python3
"""Maintainer-only tool-based image builder for experiments."""
import argparse
import importlib.util
import os
import re
from pathlib import Path
import tempfile

REPO = Path(__file__).resolve().parents[3]
spec = importlib.util.spec_from_file_location("yfc_builder", REPO / "assets/vm/firecracker/build.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
guest_path, verify_runtime = builder.guest_path, builder.verify_runtime
drop_managed_certificates, rebuild_trust_store = builder.drop_managed_certificates, builder.rebuild_trust_store


def build_image(args):
    with tempfile.TemporaryDirectory(prefix="yfc-init-") as tmp:
        args.init_binary = str(Path(tmp) / "yoloctl-init")
        env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH="amd64", GOFLAGS="")
        builder.inputs.run(["go", "build", "-buildvcs=false", "-trimpath", "-ldflags", "-s -w", "-o", args.init_binary, "./cmd/yoloctl-vm-guest"], cwd=REPO, env=env)
        args.init_sha256 = builder.inputs.digest(args.init_binary)
        args.guest_protocol = int(re.search(r"const Protocol = (\d+)", (REPO / "internal/vmwire/boot.go").read_text())[1])
        builder.build(args)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument("--kernel-build", required=True)
    parser.add_argument("--cache")
    parser.add_argument("--unsquashfs", default="unsquashfs")
    build_image(parser.parse_args())


if __name__ == "__main__":
    main()
