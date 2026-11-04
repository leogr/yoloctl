#!/usr/bin/env python3
"""Compile the bounded FUSE guest patch on pinned newer kernel baselines.

This is a source/compilation maintenance check, not a boot, libkrunfw integration
or runtime correctness test. It uses a fresh directory, verified archive cache,
an explicit prior kernel configuration, and no system installation.
"""
import argparse
import datetime
import difflib
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import tarfile

HERE = Path(__file__).resolve().parent
PINS = {
    "6.18": "9106a4605da9e31ff17659d958782b815f9591ab308d03b0ee21aad6c7dced4b",
    "7.0": "bb7f6d80b387c757b7d14bb93028fcb90f793c5c0d367736ee815a100b3891f0",
    "5fc5768c7ca92895ccd1de94dc521e5a55ae7896":
        "092bb8395739a3bee502d39c70354677a7acfc5e43bf76e6a27edfb4f577b67f",
}
REVIEWED_HEAD = "5fc5768c7ca92895ccd1de94dc521e5a55ae7896"
FILES = ["fs/fuse/dax.c", "fs/fuse/file.c", "fs/fuse/fuse_i.h",
         "fs/fuse/inode.c", "include/uapi/linux/fuse.h"]


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def replace_once(path, old, new):
    text = path.read_text()
    if text.count(old) != 1:
        raise RuntimeError("unexpected rebase context in " + str(path))
    path.write_text(text.replace(old, new, 1))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, choices=PINS)
    parser.add_argument("--cache", required=True, type=Path)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--directory", required=True, type=Path, help="must be new")
    parser.add_argument("--discard-source", action="store_true", help="remove only this new source tree after recording results")
    args = parser.parse_args()
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        parser.error("this component build check supports Linux x86_64 only")
    suffix = ".tar.gz" if args.version == REVIEWED_HEAD else ".tar.xz"
    archive = args.cache / ("linux-" + args.version + suffix)
    if digest(archive) != PINS[args.version]:
        parser.error("archive checksum differs from the recorded source pin")
    config = args.config.resolve(strict=True)
    args.directory.mkdir(mode=0o700)
    root = args.directory.resolve()
    source = root / ("linux-" + args.version)
    report = {"started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "version": args.version, "source_sha256": PINS[args.version],
              "status": "failed", "host_kernel": os.uname().release,
              "limits": "FUSE component compilation only; no boot, firmware integration or semantic qualification."}
    try:
        with tarfile.open(archive) as stream:
            stream.extractall(root, filter="data")
        original = {name: (source / name).read_text() for name in FILES}
        patch = HERE / "coordinated-fs/kernel.patch"
        result = subprocess.run(["patch", "--batch", "--fuzz=0", "-p1", "-i", str(patch)],
                                cwd=source, capture_output=True, text=True, timeout=30)
        (root / "initial-apply.log").write_text(result.stdout + result.stderr)
        rejects = sorted(str(p.relative_to(source)) for p in source.rglob("*.rej"))
        expected_rejects = ["fs/fuse/fuse_i.h.rej", "fs/fuse/inode.c.rej", "include/uapi/linux/fuse.h.rej"]
        expected_hunks = 3
        if args.version == REVIEWED_HEAD:
            expected_rejects.insert(0, "fs/fuse/file.c.rej")
            expected_hunks = 5
        if (result.returncode != 1 or rejects != expected_rejects
                or len(re.findall(r"^Hunk #[0-9]+ FAILED", result.stdout, re.MULTILINE)) != expected_hunks):
            raise RuntimeError("rebase conflicts differ from the inspected baseline")
        if args.version == REVIEWED_HEAD:
            replace_once(source / "fs/fuse/fuse_i.h", "\tunsigned writeback_cache:1;\n",
                         "\tunsigned writeback_cache:1;\n\n"
                         "\t/** @coordinated_io: experimental server-supplied write offset */\n"
                         "\tunsigned coordinated_io:1;\n")
            replace_once(source / "fs/fuse/file.c", "\targs->out_args[0].value = &ia->write.out;\n",
                         "\targs->out_args[0].value = &ia->write.out;\n"
                         "\tif (ff->fm->fc->coordinated_io) {\n"
                         "\t\tia->write.in.write_flags |= FUSE_WRITE_COORDINATED;\n"
                         "\t\targs->out_args[0].size += sizeof(ia->write.coordinated_end);\n\t}\n")
        replace_once(source / "fs/fuse/fuse_i.h",
                     "\t\t\tstruct fuse_write_out out;\n\t\t\tbool folio_locked;",
                     "\t\t\tstruct fuse_write_out out;\n\t\t\tu64 coordinated_end;\n\t\t\tbool folio_locked;")
        replace_once(source / "fs/fuse/inode.c",
                     "\t\tFUSE_REQUEST_TIMEOUT;", "\t\tFUSE_REQUEST_TIMEOUT | FUSE_COORDINATED_IO;")
        replace_once(source / "include/uapi/linux/fuse.h",
                     "#define FUSE_ASYNC_READ\t\t(1 << 0)",
                     "/* Private bounded-prototype ABI. Never use with an unpatched service. */\n"
                     "#define FUSE_COORDINATED_IO\t(1ULL << 63)\n"
                     "#define FUSE_WRITE_COORDINATED\t(1U << 31)\n\n"
                     "#define FUSE_ASYNC_READ\t\t(1 << 0)")
        report["rebase_changes"] = ["adjacent page_locked field became folio_locked",
                                     "initial capability list gained FUSE_REQUEST_TIMEOUT",
                                     "UAPI capability documentation gained entries"]
        if args.version == REVIEWED_HEAD:
            report["rebase_changes"] += ["connection field uses member kernel-doc syntax",
                                         "write argument setup gained adjacent zero_copy initialization"]
        rebased = "".join("".join(difflib.unified_diff(original[name].splitlines(True),
                            (source / name).read_text().splitlines(True), "a/" + name, "b/" + name))
                          for name in FILES)
        (root / "rebased.patch").write_text(rebased)
        report["original_files_sha256"] = {name: hashlib.sha256(text.encode()).hexdigest()
                                            for name, text in original.items()}
        report["rebased_files_sha256"] = {name: digest(source / name) for name in FILES}
        report["rebased_patch_sha256"] = digest(root / "rebased.patch")
        shutil.copyfile(config, source / ".config")
        env = os.environ.copy()
        env.update(KBUILD_BUILD_TIMESTAMP="2026-09-25 00:00:00 UTC", KBUILD_BUILD_USER="ycf-upgrade",
                   KBUILD_BUILD_HOST="component-check")
        report["cc"] = subprocess.check_output(["cc", "--version"], text=True).splitlines()[0]
        for command in [["make", "olddefconfig"], ["make", "-j2", "fs/fuse/"]]:
            with (root / "build.log").open("ab") as output:
                subprocess.run(command, cwd=source, env=env, stdout=output,
                               stderr=subprocess.STDOUT, check=True, timeout=900)
        shutil.copyfile(source / ".config", root / "resolved.config")
        enabled = set((root / "resolved.config").read_text().splitlines())
        if not {"CONFIG_FUSE_FS=y", "CONFIG_FUSE_DAX=y", "CONFIG_VIRTIO_FS=y"} <= enabled:
            raise RuntimeError("the configuration did not compile the required DAX/virtiofs paths")
        report["compiled_objects"] = {str(p.relative_to(source)): digest(p)
                                      for p in sorted((source / "fs/fuse").glob("*.o"))}
        if not all("fs/fuse/" + name + ".o" in report["compiled_objects"]
                   for name in ("dax", "file", "inode", "virtio_fs")):
            raise RuntimeError("required object files missing")
        report["kernel_config_sha256"] = digest(root / "resolved.config")
        report["status"] = "compiled"
    except Exception as error:
        report["error"] = type(error).__name__ + ": " + str(error)
    finally:
        (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        if args.discard_source and source.is_dir() and not source.is_symlink():
            shutil.rmtree(source)
    print(json.dumps(report, indent=2))
    return 0 if report["status"] == "compiled" else 1


if __name__ == "__main__":
    raise SystemExit(main())
