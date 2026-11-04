#!/usr/bin/env python3
"""Rebuild the pinned terminal artifact; ordinary Go builds do not run this."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
ASSETS = ROOT / "internal/terminal/wasm"
REVISION = "33da6848d63b3bba2b4f31ab1531d618f2795192"
SOURCE_SHA256 = "bbda18d6f6666ff05dec33974c134483b096fe321ff68a22eba9399b70354de6"
ZIG_VERSION = "0.16.0"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--zig", default=os.environ.get("ZIG", "zig"))
    parser.add_argument("--source-archive", type=Path, help="Use an already downloaded, hash-checked source archive")
    parser.add_argument("--check", action="store_true", help="Verify byte-identical reproduction without changing files")
    args = parser.parse_args()
    zig = shutil.which(args.zig)
    if not zig:
        parser.error("Zig 0.16.0 is required to regenerate the artifact")
    zig = str(Path(zig).resolve())
    version = subprocess.check_output([zig, "version"], text=True).strip()
    if version != ZIG_VERSION:
        parser.error(f"expected Zig {ZIG_VERSION}, found {version}")
    with tempfile.TemporaryDirectory(prefix="yoloctl-terminal-") as temp:
        work = Path(temp)
        archive = args.source_archive
        if archive is None:
            archive = work / "source.tar.gz"
            urllib.request.urlretrieve(f"https://codeload.github.com/ghostty-org/ghostty/tar.gz/{REVISION}", archive)
        if digest(archive) != SOURCE_SHA256:
            parser.error("Ghostty source archive hash mismatch")
        with tarfile.open(archive) as tar:
            tar.extractall(work, filter="data")
        source = work / f"ghostty-{REVISION}"
        patch = work / "native.patch"
        shutil.copyfile(ASSETS / "native.patch", patch)
        subprocess.run(["patch", "-t", "-p1", "-i", str(patch)], cwd=source, check=True)
        shutil.copyfile(ASSETS / "bridge.zig", source / "src/yolo_bridge.zig")
        with (source / "src/lib_vt.zig").open("a") as dest:
            dest.write('\ncomptime { _ = @import("yolo_bridge.zig"); }\n')
        subprocess.run([
            zig, "build", "-Demit-lib-vt=true", "-Dtarget=wasm32-freestanding",
            "-Doptimize=ReleaseSmall", "-Dversion-string=1.3.2-dev-yoloctl",
            "-Dlib-version-string=0.0.0", "--cache-dir", str(work / "cache"),
        ], cwd=source, check=True)
        artifact = source / "zig-out/bin/ghostty-vt.wasm"
        metadata = {
            "ghostty_revision": REVISION,
            "source_sha256": SOURCE_SHA256,
            "zig_version": ZIG_VERSION,
            # Record the copied input that was compiled, even if the working
            # tree changes while the build is running.
            "bridge_sha256": digest(source / "src/yolo_bridge.zig"),
            "patch_sha256": digest(patch),
            "artifact_sha256": digest(artifact),
            "abi": 6,
        }
        if args.check:
            if artifact.read_bytes() != (ASSETS / "ghostty-vt.wasm").read_bytes():
                parser.error("rebuilt terminal artifact differs")
            if json.loads((ASSETS / "manifest.json").read_text()) != metadata:
                parser.error("terminal manifest differs")
            print("Terminal artifact reproduced byte for byte")
        else:
            shutil.copyfile(artifact, ASSETS / "ghostty-vt.wasm")
            (ASSETS / "manifest.json").write_text(json.dumps(metadata, indent=2) + "\n")
            print(f"Terminal artifact: {metadata['artifact_sha256']}")


if __name__ == "__main__":
    main()
