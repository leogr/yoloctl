"""Verified native CLI installation; invoked only for image/tool maintenance."""
import fcntl
import gzip
import hashlib
import io
import json
import os
import pathlib
import platform
import re
import shutil
import sys
import tarfile
import tempfile
import urllib.request


def read_manifest(url):
    with urllib.request.urlopen(url, timeout=30) as response:
        raw = response.read(1024 * 1024 + 1)
    if len(raw) > 1024 * 1024:
        raise ValueError("release manifest too large")
    if raw.startswith(b"\x1f\x8b"):
        with gzip.GzipFile(fileobj=io.BytesIO(raw)) as decompressed:
            raw = decompressed.read(1024 * 1024 + 1)
        if len(raw) > 1024 * 1024:
            raise ValueError("decompressed manifest too large")
    return json.loads(raw)


def install(spec):
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
    if platform.system() != "Linux" or arch is None:
        raise ValueError("native release requires Linux amd64 or arm64")
    binary = pathlib.Path(spec["binary"])
    binary.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Serialize updates before fetching latest, so a delayed older request
    # cannot replace a newer installation. All writes run as sandbox at runtime.
    with open(str(binary) + ".lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        manifest = read_manifest(spec["manifest_base"] + arch + ".json")
        if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?", manifest["version"]):
            raise ValueError("invalid native version")
        if not re.fullmatch(r"[a-fA-F0-9]{128}", manifest["sha512"]):
            raise ValueError("invalid SHA512")
        if not manifest["url"].startswith(spec["artifact_base"]):
            raise ValueError("artifact URL outside distribution")
        with tempfile.TemporaryDirectory(prefix=".install-", dir=binary.parent) as temp:
            temp = pathlib.Path(temp)
            archive = temp / "release.tar.gz"
            digest = hashlib.sha512()
            size = 0
            with urllib.request.urlopen(manifest["url"], timeout=60) as source, archive.open("wb") as dest:
                if not source.geturl().startswith(spec["artifact_base"]):
                    raise ValueError("artifact redirect outside distribution")
                while chunk := source.read(1024 * 1024):
                    size += len(chunk)
                    if size > 512 * 1024 * 1024:
                        raise ValueError("native archive too large")
                    digest.update(chunk)
                    dest.write(chunk)
            if digest.hexdigest() != manifest["sha512"].lower():
                raise ValueError("native archive SHA512 mismatch")
            staged = temp / "binary"
            # Copy exactly one regular member; never extract archive paths,
            # symlinks, owners, devices or permissions into the container.
            with tarfile.open(archive, "r:gz") as tar:
                member = tar.getmember(spec["member"])
                if not member.isfile() or member.size > 1024 * 1024 * 1024:
                    raise ValueError("invalid native binary archive member")
                with tar.extractfile(member) as source, staged.open("wb") as dest:
                    shutil.copyfileobj(source, dest)
                    os.fchmod(dest.fileno(), 0o755)
                    dest.flush()
                    os.fsync(dest.fileno())
            metadata = temp / "metadata.json"
            with metadata.open("w") as out:
                json.dump({"version": manifest["version"], "sha512": manifest["sha512"]}, out)
                out.flush()
                os.fsync(out.fileno())
            # Invalidate metadata before replacing the executable. An interrupted
            # update reports unknown, never a new version for the old binary.
            directory = os.open(binary.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                pathlib.Path(str(binary) + ".json").unlink(missing_ok=True)
                os.fsync(directory)
                os.replace(staged, binary)
                os.fsync(directory)
                os.replace(metadata, str(binary) + ".json")
                os.fsync(directory)
            finally:
                os.close(directory)
        print(manifest["version"])


if __name__ == "__main__":
    install(json.loads(sys.argv[1]))
