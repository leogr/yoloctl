#!/usr/bin/env bash
# Shared by reusable QA and release packaging on GitHub-hosted Linux/macOS.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tool_dir="${RUNNER_TEMP:?}/yoloctl-openshell-tools"

# Rustup is provided by the GitHub-hosted images. Select the repo's Rust pin
# and both guest targets; never depend on the runner's default Rust release.
rust_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["rust"])' "$repo_root/third_party/openshell/pins.json")"
rustup toolchain install "$rust_version" --profile minimal \
  --target x86_64-unknown-linux-musl,aarch64-unknown-linux-musl

# Official Python wheels provide the same tools on both host platforms.
# Keep infra versions explicit; no source compilation or global pip install.
python3 -m venv "$tool_dir"
"$tool_dir/bin/python" -m pip install --disable-pip-version-check --only-binary=:all: \
  ziglang==0.15.2 cargo-zigbuild==0.23.4
ln -s "$tool_dir/bin/python-zig" "$tool_dir/bin/zig"
export PATH="$tool_dir/bin:$PATH"
printf '%s\n' "$tool_dir/bin" >> "${GITHUB_PATH:?}"
rustup run "$rust_version" rustc --version
zig version
cargo-zigbuild --version
