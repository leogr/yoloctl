#!/usr/bin/env python3
"""Build and qualify the bundled Linux 7 recipe in an isolated image.

The starting Firecracker configuration targets Amazon's 6.18 tree. olddefconfig
adapts it to upstream Linux; the effective diff and required-feature checks are
part of this recipe, not a claim of upstream Firecracker support.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
VERSION = "7.2.11"
SOURCE = f"linux-{VERSION}.tar.xz"
# https://cdn.kernel.org/pub/linux/kernel/v7.x/sha256sums.asc (2026-10-16)
SOURCE_PIN = {
    "url": f"https://cdn.kernel.org/pub/linux/kernel/v7.x/{SOURCE}",
    "sha256": "fd0fc479f983a8c05313f13e9c36330fe6651cf725396748c40d5cd93fa1e6a4",
}
CONFIG = "kernel-6.18.config"
REQUIRED = [
    "64BIT", "X86_64", "ACPI", "PCI", "KVM_GUEST", "SERIAL_8250_CONSOLE",
    "VIRTIO_MMIO", "VIRTIO_BLK", "VIRTIO_NET", "VIRTIO_VSOCKETS",
    "DEVTMPFS", "PROC_FS", "SYSFS", "TMPFS", "UNIX", "EXT4_FS", "BLK_DEV_LOOP",
    "OVERLAY_FS", "FUSE_FS", "TUN", "NAMESPACES", "USER_NS", "PID_NS", "NET_NS",
    "VETH", "BRIDGE", "NF_TABLES", "CGROUPS", "MEMCG", "CGROUP_PIDS",
    "CGROUP_SCHED", "FAIR_GROUP_SCHED", "CFS_BANDWIDTH", "BLK_CGROUP", "PSI",
    "SECCOMP", "SECCOMP_FILTER", "SECURITY", "SECURITY_LANDLOCK",
]


def load_builder():
    spec = importlib.util.spec_from_file_location("linux7_image_builder", HERE / "build.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def validate_config(config, inputs):
    inputs.validate_config(config)

def verify_kernel(path, inputs):
    manifest = json.loads((path / "kernel.json").read_text())
    if (manifest.get("inputs") != {name: inputs.PINS[name] for name in sorted(inputs.KERNEL_INPUTS)}
            or manifest.get("enabled") != inputs.KERNEL_EXTRAS
            or manifest.get("disabled") != inputs.KERNEL_DISABLED
            or manifest.get("kernel_sha256") != inputs.digest(path / "kernel")
            or manifest.get("config_sha256") != inputs.digest(path / "kernel.config")):
        raise RuntimeError("kernel does not match the pinned Linux 7 recipe")
    validate_config((path / "kernel.config").read_text(), inputs)


def kernel(args, inputs):
    inputs.kernel(args)
    verify_kernel(Path(args.output), inputs)

def image(args, module):
    verify_kernel(Path(args.kernel_build), module.builder.inputs)
    module.build_image(args)
    shutil.copyfile(Path(args.kernel_build) / "kernel.config", Path(args.output) / "kernel.config")


def verify_image(path, inputs):
    provenance = json.loads((path / "provenance.json").read_text())
    manifest = provenance["kernel"]
    image_manifest = json.loads((path / "image.json").read_text())
    if (manifest.get("inputs") != {name: inputs.PINS[name] for name in sorted(inputs.KERNEL_INPUTS)}
            or manifest.get("enabled") != inputs.KERNEL_EXTRAS
            or manifest.get("disabled") != inputs.KERNEL_DISABLED
            or manifest.get("kernel_sha256") != inputs.digest(path / "kernel")
            or manifest.get("config_sha256") != inputs.digest(path / "kernel.config")
            or image_manifest.get("kernel_sha256") != manifest.get("kernel_sha256")):
        raise RuntimeError("image does not contain the pinned Linux 7 recipe")
    validate_config((path / "kernel.config").read_text(), inputs)
    # The normal CLI verifies the other immutable image members before boot.


def smoke(args, inputs):
    inputs.require_platform()
    image_path = Path(args.image).resolve(strict=True)
    verify_image(image_path, inputs)
    executable = Path(args.yoloctl).resolve(strict=True)
    out = inputs.new_directory(args.output)
    env = {k: v for k, v in os.environ.items() if not k.startswith("SMOKE_")}
    env.update(YOLOCTL=str(executable), SMOKE_ROOT=str(out),
               SMOKE_SPACE="smoke-linux7", SMOKE_WORKDIR=str(out / "workdir"),
               SMOKE_BACKEND="firecracker", SMOKE_IMPORT_BACKEND="firecracker",
               SMOKE_VM_IMAGE=str(image_path), SMOKE_UPDATE_VM_IMAGE=str(image_path),
               SMOKE_EXPECT_KERNEL_RELEASE=VERSION, SMOKE_KEEP_ON_FAILURE="1")
    # Both image selectors are intentional: space-lifecycle otherwise replaces
    # a supplied candidate with the CLI's bundled kernel partway through the gate.
    inputs.run(["bash", REPO / "scripts/smoke/linux7.sh"], cwd=REPO, env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    compile_cmd = commands.add_parser("kernel", help="compile the pinned candidate without installing it")
    compile_cmd.add_argument("--jobs", type=int, default=2)
    compile_cmd.add_argument("--output", required=True, help="new build directory")
    compile_cmd.add_argument("--cache", help="optional shared checksum-addressed download cache")
    compile_cmd.add_argument("--cc", help="explicit GCC 13.3.0 executable")
    compile_cmd.add_argument("--cross-compile", default="", help="x86_64 cross-tool prefix for non-x86 Linux hosts")
    compile_cmd.add_argument("--reproduce", action="store_true", help="two object directories must produce identical kernel/config")
    image_cmd = commands.add_parser("image", help="assemble Ubuntu userspace with the verified candidate")
    image_cmd.add_argument("--kernel-build", required=True)
    image_cmd.add_argument("--output", required=True, help="new image directory")
    image_cmd.add_argument("--cache")
    image_cmd.add_argument("--unsquashfs", default="unsquashfs")
    smoke_cmd = commands.add_parser("smoke", help="qualify a verified candidate in a fresh isolated host VM")
    smoke_cmd.add_argument("--image", required=True)
    smoke_cmd.add_argument("--output", required=True, help="new persistent fixture/report directory")
    smoke_cmd.add_argument("--yoloctl", default=str(REPO / "bin/yoloctl"))
    args = parser.parse_args()
    module = load_builder()
    if args.command == "kernel":
        kernel(args, module.builder.inputs)
    elif args.command == "image":
        image(args, module)
    else:
        smoke(args, module.builder.inputs)


if __name__ == "__main__":
    main()
