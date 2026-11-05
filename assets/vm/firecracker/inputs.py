"""Maintainer input acquisition and kernel compilation recipe."""
import difflib
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
PINS = json.loads((HERE / "pins.json").read_text())
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
    # Built-in KVM lets privileged development boxes run nested Firecracker
    # without a guest module tree.
    "VIRTUALIZATION", "KVM", "KVM_INTEL", "KVM_AMD",
]

KERNEL_INPUTS = {SOURCE, CONFIG}
KERNEL_EXTRAS = ["NF_TABLES_INET", "NFT_REJECT", "NFT_REJECT_INET", "NFT_REJECT_IPV4", "NFT_REJECT_IPV6",
                 "NFT_LOG", "NFT_LIMIT", "NF_LOG_SYSLOG", "SECURITY_LANDLOCK", "IKCONFIG", "IKCONFIG_PROC",
                 "NFT_MASQ", "NFT_REDIR", "NFT_FIB_IPV4", "NFT_FIB_IPV6", "NFT_FIB_INET",
                 "NETFILTER_XT_MATCH_COMMENT", "NETFILTER_XT_MATCH_MARK", "NETFILTER_XT_MATCH_MULTIPORT",
                 "NETFILTER_XT_MATCH_STATISTIC", "NETFILTER_XT_MATCH_STATE", "NETFILTER_XT_MATCH_LIMIT",
                 "NETFILTER_XT_TARGET_MARK", "NETFILTER_XT_TARGET_TCPMSS"]
KERNEL_EXTRAS = sorted(set(KERNEL_EXTRAS + REQUIRED + [
 "BPF", "BPF_SYSCALL", "BPF_JIT", "BPF_EVENTS", "PERF_EVENTS",
 "KPROBES", "KPROBE_EVENTS", "UPROBES", "UPROBE_EVENTS", "FTRACE",
 "FTRACE_SYSCALLS", "TRACEPOINTS", "DEBUG_INFO_DWARF4", "DEBUG_INFO_BTF",
]))
KERNEL_DISABLED = ["PSI_DEFAULT_DISABLED", "LOCALVERSION_AUTO", "DEBUG_INFO_NONE"]
# Linux 7's flags for pinned pahole 1.25, with serial encoding: its parallel
# optimized-function deduplication changes type IDs between identical builds.
KERNEL_PAHOLE_FLAGS = "--btf_gen_floats -j1 --skip_encoding_btf_inconsistent_proto --btf_gen_optimized --lang_exclude=rust"


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def save_json(path, value):
    # Exclusive output; a previous report/build is never overwritten.
    with Path(path).open("x") as stream:
        json.dump(value, stream, indent=2)
        stream.write("\n")


def run(argv, **kwargs):
    return subprocess.run([str(x) for x in argv], check=True, **kwargs)


def new_directory(path):
    path = Path(path).absolute()
    path.mkdir(mode=0o700, parents=False, exist_ok=False)
    return path.resolve()


def download(name, destination, cache=None):
    pin = PINS[name]
    target = Path(destination) / name
    cached = None
    if cache:
        cache = Path(cache)
        cache.mkdir(parents=True, exist_ok=True)
        # Digest keys survive recipe updates; named inputs also support an
        # explicitly preseeded offline cache from the development builder.
        cached = next((p for p in [cache / pin["sha256"], cache / name] if p.exists()), None)
    if cached:
        if cached.is_symlink() or not cached.is_file() or digest(cached) != pin["sha256"]:
            raise RuntimeError(f"cached {name} checksum mismatch")
        shutil.copyfile(cached, target)
    else:
        print(f"Fetching pinned {name}", flush=True)
        if not pin["url"].startswith("https://"):
            raise RuntimeError("pinned inputs require HTTPS")
        with urllib.request.urlopen(pin["url"], timeout=60) as src, target.open("xb") as dst:
            if not src.url.startswith("https://"):
                raise RuntimeError("input redirect must remain HTTPS")
            shutil.copyfileobj(src, dst)
    if digest(target) != pin["sha256"]:
        raise RuntimeError(f"{name} checksum mismatch; refusing to use it")
    if cache and not cached:
        fd, temporary = tempfile.mkstemp(prefix=".download-", dir=cache)
        try:
            with os.fdopen(fd, "wb") as dst, target.open("rb") as src:
                shutil.copyfileobj(src, dst)
                dst.flush()
                os.fsync(dst.fileno())
            os.replace(temporary, cache / pin["sha256"])
        finally:
            Path(temporary).unlink(missing_ok=True)
    return target


def tar_file(archive, member, destination):
    # Never extract archive-supplied paths or links onto the host.
    with tarfile.open(archive, "r:gz") as tar:
        entry = tar.getmember(member)
        if not entry.isfile():
            raise RuntimeError(f"not a regular archive member: {member}")
        with tar.extractfile(entry) as src, destination.open("xb") as dst:
            shutil.copyfileobj(src, dst)
    destination.chmod(0o755)


def require_platform():
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise RuntimeError("image preparation requires a Linux x86_64 host")
    if os.geteuid() == 0:
        raise RuntimeError("run as your normal user, with access to /dev/kvm; do not use sudo")


def require_tools(names):
    for name in names:
        if not shutil.which(str(name)):
            raise RuntimeError(f"missing prerequisite: {name}")


def publish_kernel(out, build, cc="gcc", cross=""):
    config = (build / ".config").read_text()
    validate_config(config)
    for option in KERNEL_EXTRAS:
        if f"CONFIG_{option}=y" not in config.splitlines():
            raise RuntimeError(f"custom kernel configuration lacks {option}")
    for option in KERNEL_DISABLED:
        if f"# CONFIG_{option} is not set" not in config.splitlines():
            raise RuntimeError(f"unsupported optional kernel module enabled: {option}")
    # BTF stays built into the loadable kernel; only standalone DWARF sections
    # are removed after pahole encoded them, keeping the embedded ELF bounded.
    run([cross + "objcopy", "--strip-debug", build / "vmlinux", out / "kernel"])
    toolchain = {}
    for name, binary in [("cc", cc), ("pahole", os.environ.get("PAHOLE", "pahole")),
                         ("ld", cross + "ld"), ("objcopy", cross + "objcopy")]:
        executable = Path(shutil.which(binary) or binary).resolve(strict=True)
        version = run([binary, "--version"], capture_output=True, text=True).stdout.splitlines()[0]
        toolchain[name] = {"version": version, "sha256": digest(executable)}
    sections = run([cross + "readelf", "-S", out / "kernel"], capture_output=True, text=True).stdout
    if ".BTF" not in sections or ".debug_info" in sections:
        raise RuntimeError("bundled kernel must retain BTF and omit standalone DWARF")
    shutil.copyfile(build / ".config", out / "kernel.config")
    save_json(out / "kernel.json", {"schema": 1,
              "inputs": {name: PINS[name] for name in sorted(KERNEL_INPUTS)},
              "enabled": KERNEL_EXTRAS, "disabled": KERNEL_DISABLED, "kernel_sha256": digest(out / "kernel"),
              "config_sha256": digest(out / "kernel.config"),
              "compiler": toolchain["cc"]["version"], "build_tools": toolchain,
              "pahole_flags": KERNEL_PAHOLE_FLAGS})


def validate_config(config):
    lines = config.splitlines()
    for name in KERNEL_EXTRAS:
        if f"CONFIG_{name}=y" not in lines:
            raise RuntimeError(f"Linux 7 configuration lacks {name}")
    for name in KERNEL_DISABLED:
        if f"# CONFIG_{name} is not set" not in lines:
            raise RuntimeError(f"Linux 7 configuration unexpectedly enables {name}")
    lsm = next((line.removeprefix('CONFIG_LSM="').removesuffix('"').split(',')
                for line in lines if line.startswith('CONFIG_LSM="')), [])
    if "landlock" not in lsm:
        raise RuntimeError("Linux 7 configuration does not activate Landlock")


def kernel(args):
    if platform.system() != "Linux" or os.geteuid() == 0:
        raise RuntimeError("kernel compilation requires a normal Linux user; it needs no VM/KVM")
    cross = getattr(args, "cross_compile", "")
    if platform.machine() != "x86_64" and not cross:
        raise RuntimeError("non-x86 hosts must explicitly select an x86_64 cross-compiler prefix")
    cc = getattr(args, "cc", None) or (cross + "gcc" if cross else "gcc")
    require_tools([cc, cross + "objcopy", cross + "readelf", "make", "tar", "xz", "flex", "bison", "m4", "bc", "pahole"])
    if run([cc, "-dumpfullversion"], capture_output=True, text=True).stdout.strip() != "13.3.0":
        raise RuntimeError("kernel recipe requires GCC 13.3.0")
    if run([os.environ.get("PAHOLE", "pahole"), "--version"], capture_output=True, text=True).stdout.strip() != "v1.25":
        raise RuntimeError("kernel recipe requires pinned pahole v1.25")
    if not 1 <= args.jobs <= 64:
        raise RuntimeError("kernel build jobs must be between 1 and 64")
    out = new_directory(args.output)
    downloads, build = out / "inputs", out / "build"
    downloads.mkdir()
    build.mkdir()
    for name in sorted(KERNEL_INPUTS):
        download(name, downloads, args.cache)
    # Only the checksum-verified upstream archive is extracted. No host
    # installation, root privileges or VM access is involved in compilation.
    run(["tar", "--no-same-owner", "-xf", downloads / SOURCE, "-C", out])
    source = out / f"linux-{VERSION}"
    shutil.copyfile(downloads / CONFIG, build / ".config")
    configure = [source / "scripts/config", "--file", build / ".config"]
    for name in KERNEL_EXTRAS:
        run([*configure, "--enable", name])
    for name in KERNEL_DISABLED:
        run([*configure, "--disable", name])
    run([*configure, "--set-str", "LOCALVERSION", ""])
    # Stable build metadata; provenance still records the actual compiler.
    env = dict(os.environ, KBUILD_BUILD_USER="yoloctl", KBUILD_BUILD_HOST="linux7",
               KBUILD_BUILD_TIMESTAMP="2026-10-15 00:00:00 UTC", KBUILD_BUILD_VERSION="1")
    make = ["make", "-C", source, f"O={build}", "ARCH=x86", f"CC={cc}", f"CROSS_COMPILE={cross}",
            f"PAHOLE_FLAGS={KERNEL_PAHOLE_FLAGS}"]
    # Prefix maps keep BTF/DWARF and __FILE__ independent of owned build paths.
    common_flags = f"-ffile-prefix-map={source}=/src -fdebug-prefix-map={source}=/src -ffile-prefix-map={build}=/build -fdebug-prefix-map={build}=/build"
    env["KCFLAGS"] = common_flags
    env["KAFLAGS"] = common_flags
    print(f"Linux {VERSION} build log: {out / 'build.log'}", flush=True)
    with (out / "build.log").open("x") as log:
        run([*make, "olddefconfig"], env=env, stdout=log, stderr=log)
        config = (build / ".config").read_text()
        (out / "config.diff").write_text(''.join(difflib.unified_diff(
            (downloads / CONFIG).read_text().splitlines(keepends=True),
            config.splitlines(keepends=True), fromfile="firecracker-6.18.config",
            tofile=f"linux-{VERSION}.config")))
        validate_config(config)
        release = run([*make, "-s", "kernelrelease"], env=env,
                             capture_output=True, text=True).stdout.strip()
        if release != VERSION:
            raise RuntimeError(f"unexpected kernel release: {release!r}")
        run([*make, "-j", args.jobs, "vmlinux"], env=env, stdout=log, stderr=log)
    publish_kernel(out, build, cc, cross)
    if getattr(args, "reproduce", False):
        second = out / "reproduce-build"
        second.mkdir()
        shutil.copyfile(build / ".config", second / ".config")
        second_flags = common_flags.replace(str(build), str(second))
        second_env = dict(env, KCFLAGS=second_flags, KAFLAGS=second_flags)
        second_make = ["make", "-C", source, f"O={second}", "ARCH=x86", f"CC={cc}", f"CROSS_COMPILE={cross}",
                       f"PAHOLE_FLAGS={KERNEL_PAHOLE_FLAGS}"]
        with (out / "reproduce.log").open("x") as log:
            run([*second_make, "olddefconfig"], env=second_env, stdout=log, stderr=log)
            run([*second_make, "-j", args.jobs, "vmlinux"], env=second_env, stdout=log, stderr=log)
        run([cross + "objcopy", "--strip-debug", second / "vmlinux", out / "reproduced-kernel"])
        if digest(out / "kernel") != digest(out / "reproduced-kernel") or digest(build / ".config") != digest(second / ".config"):
            raise RuntimeError("kernel/config differ between independent object-directory builds")
        save_json(out / "reproducibility.json", {"reproducible": True, "kernel_sha256": digest(out / "kernel"), "config_sha256": digest(out / "kernel.config")})
    print(f"Prepared Linux {VERSION} kernel: {out}")
