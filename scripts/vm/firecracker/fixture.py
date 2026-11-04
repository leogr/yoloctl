#!/usr/bin/env python3
"""Prepare an offline generic-shell OCI fixture for the isolated VM smoke.

This is test preparation, not a production box image or a timed startup step.
The source must be the prepared microVM Ubuntu staging tree (no real account state).
"""
import argparse
import hashlib
import gzip
import shutil
import io
import json
from pathlib import Path
import tarfile

REPO = Path(__file__).resolve().parents[3]


def build(source, output):
    source, output = Path(source).resolve(), Path(output).resolve()
    output.mkdir(mode=0o700, exist_ok=False)
    layer = output / "layer.tar"
    excluded = {"poc", "poc-workdir", "root", "var", "dev", "run", "proc", "sys", "tmp", "home", "usr/local", "usr/sbin/yoloctl-init", "opt", "usr/lib/python3", "usr/lib/udev", "usr/lib/systemd", "usr/share/doc", "usr/share/man"}
    def include(info):
        name = info.name.rstrip("/")
        if any(name == p or name.startswith(p + "/") for p in excluded):
            return None
        if name in ("etc/passwd", "etc/group"):
            return None
        info.uid = info.gid = 0
        info.uname = info.gname = "root"
        info.mtime = 0
        return info
    with tarfile.open(layer, "w") as tar:
        for item in sorted(source.iterdir()):
            tar.add(item, arcname=item.name, recursive=True, filter=include)
        for name in ["dev", "run", "run/lock", "proc", "sys", "tmp", "var", "var/log", "var/tmp", "root", "home", "home/sandbox", "workdir", "usr/local", "usr/local/bin", "opt"]:
            entry = tarfile.TarInfo(name)
            entry.type, entry.mode = tarfile.DIRTYPE, 0o755
            if name in ("tmp", "var/tmp"):
                entry.mode = 0o1777
            if name in ("home/sandbox", "workdir"):
                entry.uid = entry.gid = 2000
            tar.addfile(entry)
        for name, target in [("var/run", "/run"), ("var/lock", "/run/lock")]:
            entry = tarfile.TarInfo(name)
            entry.type, entry.linkname, entry.mode = tarfile.SYMTYPE, target, 0o777
            tar.addfile(entry)
        def add(name, data, mode=0o644):
            entry = tarfile.TarInfo(name)
            entry.mode, entry.size = mode, len(data)
            tar.addfile(entry, io.BytesIO(data))
        for name, line in [("passwd", "sandbox:x:2000:2000:sandbox:/home/sandbox:/bin/bash"), ("group", "sandbox:x:2000:")]:
            lines = [line for line in (source / "etc" / name).read_text().splitlines() if line.split(":")[2] not in ("1000", "2000")]
            lines.append(line)
            add("etc/" + name, ("\n".join(lines) + "\n").encode())
        add("var/forbidden.txt", b"DAC-readable Landlock denial fixture\n", 0o666)
        add("usr/local/bin/yoloctl-entrypoint", (REPO / "assets/containers/agentbox/yoloctl-entrypoint.sh").read_bytes(), 0o755)
    blobs = output / "blobs/sha256"
    blobs.mkdir(parents=True)
    def digest(path):
        with path.open("rb") as f:
            return hashlib.file_digest(f, "sha256").hexdigest()
    layer_hash = digest(layer)
    compressed=output/"layer.tar.gz"
    with layer.open("rb") as src,compressed.open("xb") as dst:
        with gzip.GzipFile(fileobj=dst,mode="wb",compresslevel=1,mtime=0) as gz:
            shutil.copyfileobj(src,gz)
    compressed_hash=digest(compressed)
    compressed.rename(blobs/compressed_hash)
    layer.unlink()
    def blob(value, media):
        raw = json.dumps(value, separators=(",", ":")).encode()
        sha = hashlib.sha256(raw).hexdigest()
        (blobs / sha).write_bytes(raw)
        return {"mediaType": media, "digest": "sha256:" + sha, "size": len(raw)}
    config = blob({"architecture": "amd64", "os": "linux", "config": {"Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"], "WorkingDir": "/workdir"}, "rootfs": {"type": "layers", "diff_ids": ["sha256:" + layer_hash]}}, "application/vnd.oci.image.config.v1+json")
    manifest = blob({"schemaVersion": 2, "config": config, "layers": [{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + compressed_hash, "size": (blobs / compressed_hash).stat().st_size}]}, "application/vnd.oci.image.manifest.v1+json")
    manifest["annotations"] = {"org.opencontainers.image.ref.name": "docker.io/library/yfc-fixture:latest"}
    (output / "index.json").write_text(json.dumps({"schemaVersion": 2, "manifests": [manifest]}))
    (output / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}\n')
    archive = output / "fixture.oci.tar"
    with tarfile.open(archive, "w") as tar:
        for name in ("blobs", "index.json", "oci-layout"):
            tar.add(output / name, arcname=name)
    print(archive)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    build(args.source, args.output)
