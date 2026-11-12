#!/usr/bin/env python3
"""Rebuild the pinned Mermaid WASI artifact; normal Go builds use embedded bytes."""

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
PINS = ROOT / "third_party/mermaid-text"
ASSETS = ROOT / "internal/mermaid/wasm"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_inputs():
    pins = json.loads((PINS / "pins.json").read_text())
    for name, expected in pins["inputs"].items():
        path = PINS / name
        if digest(path) != expected:
            raise ValueError(f"Mermaid input differs from pins: {name}")
    for patch in pins["patches"]:
        if digest(PINS / patch["file"]) != patch["sha256"]:
            raise ValueError(f"Mermaid patch differs from pins: {patch['file']}")
    if digest(Path(__file__).resolve()) != pins["builder_sha256"]:
        raise ValueError("Mermaid builder differs from pins")
    recipe = dict(pins)
    recipe.pop("artifact_sha256", None)
    key = hashlib.sha256(json.dumps(recipe, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return pins, key


def prepare(work, archive, pins):
    if digest(archive) != pins["upstream"]["sha256"]:
        raise ValueError("Mermaid source archive hash mismatch")
    crates = work / "source/crates"
    crates.mkdir(parents=True)
    with tarfile.open(archive) as source:
        source.extractall(crates, filter="data")
    crate = crates / f"mermaid-text-{pins['upstream']['version']}"
    crate.rename(crates / "mermaid-text")
    crate = crates / "mermaid-text"
    # The registry-normalized manifest disables automatic integration tests.
    # Restore the archive's upstream manifest, so our added tests are exercised
    # by maintenance qualification too. Runtime dependency versions stay locked.
    shutil.copyfile(crate / "Cargo.toml.orig", crate / "Cargo.toml")
    for patch in pins["patches"]:
        subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(PINS / patch["file"])],
                       cwd=work / "source", check=True)
    for name in pins["inputs"]:
        if name.startswith("worker/"):
            target = work / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(PINS / name, target)


def build_env(work, pins, key, rustc):
    env = os.environ.copy()
    for name in list(env):
        if name.startswith("CARGO_PROFILE_") or name in {
            "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS", "RUSTC_WORKSPACE_WRAPPER",
            "CARGO_BUILD_RUSTFLAGS", "CARGO_TARGET_WASM32_WASIP1_RUSTFLAGS",
        }:
            env.pop(name)
    env.update(RUSTUP_TOOLCHAIN=pins["rust"], RUSTC=rustc,
               RUSTC_WRAPPER=str(PINS / "rustc-repro.py"),
               YOLOCTL_MERMAID_RECIPE_HASH=key,
               CARGO_INCREMENTAL="0", CARGO_BUILD_JOBS="2",
               CARGO_TARGET_DIR=str(work / "worker/target"),
               SOURCE_DATE_EPOCH=str(pins["source_date_epoch"]), LC_ALL="C", TZ="UTC")
    remaps = [(str(work), "/yoloctl-mermaid")]
    for name, default, mapped in [("CARGO_HOME", ".cargo", "/cargo"),
                                  ("RUSTUP_HOME", ".rustup", "/rustup")]:
        remaps.append((str(Path(env.get(name, Path.home() / default)).resolve()), mapped))
    # The unit separator avoids shell splitting for homes/build roots with spaces.
    env["CARGO_ENCODED_RUSTFLAGS"] = "\x1f".join(f"--remap-path-prefix={a}={b}" for a, b in remaps)
    return env


def collect_licenses(metadata, destination):
    packages = {p["id"]: p for p in metadata["packages"]}
    nodes = {n["id"]: n for n in metadata["resolve"]["nodes"]}
    seen, pending = set(), [metadata["resolve"]["root"]]
    while pending:
        key = pending.pop()
        if key in seen:
            continue
        seen.add(key)
        pending.extend(d["pkg"] for d in nodes[key]["deps"])
    result = []
    for key in sorted(seen):
        package = packages[key]
        if not package["source"]:  # Our wrapper and mermaid's separately retained upstream LICENSE.
            continue
        base = Path(package["manifest_path"]).parent
        files = sorted(p for p in base.iterdir() if p.is_file() and
                       p.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "UNLICENSE")))
        if not files:
            raise ValueError(f"No license files found for {package['name']}")
        entry = {"name": package["name"], "version": package["version"],
                 "license": package["license"], "files": {}}
        target = destination / f"{package['name']}-{package['version']}"
        target.mkdir(parents=True, exist_ok=True)
        for path in files:
            shutil.copyfile(path, target / path.name)
            entry["files"][path.name] = digest(path)
        result.append(entry)
    (destination / "manifest.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cargo", default=os.environ.get("CARGO", "cargo"))
    parser.add_argument("--rustc", default=os.environ.get("RUSTC", "rustc"))
    parser.add_argument("--source-archive", type=Path)
    parser.add_argument("--check", action="store_true", help="Require byte-identical artifact/manifest; write nothing")
    parser.add_argument("--collect-licenses", action="store_true", help="Refresh retained transitive notices")
    args = parser.parse_args()
    if args.check and args.collect_licenses:
        parser.error("--check cannot update licenses")
    pins, key = load_inputs()
    cargo, rustc = shutil.which(args.cargo), shutil.which(args.rustc)
    if not cargo or not rustc:
        parser.error(f"Rust {pins['rust']} and Cargo are required only for maintenance regeneration")
    version_env = dict(os.environ, RUSTUP_TOOLCHAIN=pins["rust"])
    for tool, name in [(cargo, "cargo"), (rustc, "rustc")]:
        version = subprocess.check_output([tool, "--version"], env=version_env, text=True)
        if not version.startswith(f"{name} {pins['rust']} "):
            parser.error(f"Expected {name} {pins['rust']}, found {version.strip()}")
    with tempfile.TemporaryDirectory(prefix="yoloctl-mermaid-") as temp:
        work = Path(temp).resolve()
        archive = args.source_archive
        if archive is None:
            archive = work / "source.crate"
            urllib.request.urlretrieve(pins["upstream"]["url"], archive)
        prepare(work, archive.resolve(), pins)
        env = build_env(work, pins, key, rustc)
        if args.collect_licenses:
            raw = subprocess.check_output([cargo, "metadata", "--format-version", "1", "--locked",
                                           "--filter-platform", pins["target"], "--manifest-path",
                                           str(work / "worker/Cargo.toml")], env=env)
            collect_licenses(json.loads(raw), PINS / "licenses")
            for path in sorted((PINS / "licenses").rglob("*")):
                if path.is_file():
                    pins["inputs"][str(path.relative_to(PINS))] = digest(path)
            (PINS / "pins.json").write_text(json.dumps(pins, indent=2, sort_keys=True) + "\n")
            pins, key = load_inputs()
            env = build_env(work, pins, key, rustc)
        command = [cargo, "build", "--release", "--locked", "--target", pins["target"],
                   "--manifest-path", str(work / "worker/Cargo.toml")]
        subprocess.run(command, env=env, check=True)
        artifact = work / f"worker/target/{pins['target']}/release/yoloctl-mermaid-wasi.wasm"
        metadata = {"schema": 1, "recipe_sha256": key, "artifact_sha256": digest(artifact),
                    "rust": pins["rust"], "target": pins["target"], "inputs": pins["inputs"],
                    "patches": pins["patches"], "source_sha256": pins["upstream"]["sha256"]}
        if args.check:
            if artifact.read_bytes() != (ASSETS / "mermaid.wasm").read_bytes():
                parser.error(f"Rebuilt Mermaid differs: {metadata['artifact_sha256']}")
            if json.loads((ASSETS / "manifest.json").read_text()) != metadata:
                parser.error("Mermaid manifest differs")
            if pins["artifact_sha256"] != metadata["artifact_sha256"]:
                parser.error("Mermaid artifact hash differs from pins")
            print("Mermaid artifact reproduced byte for byte")
        else:
            ASSETS.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(artifact, ASSETS / "mermaid.wasm")
            (ASSETS / "manifest.json").write_text(json.dumps(metadata, indent=2, sort_keys=True) + "\n")
            pins["artifact_sha256"] = metadata["artifact_sha256"]
            (PINS / "pins.json").write_text(json.dumps(pins, indent=2, sort_keys=True) + "\n")
            print(f"Mermaid artifact: {metadata['artifact_sha256']}")


if __name__ == "__main__":
    main()
