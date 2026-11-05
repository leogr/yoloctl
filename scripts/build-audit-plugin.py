#!/usr/bin/env python3
"""Regenerate/check the small pinned Falco extraction plugin, without a VM.

Ordinary Go builds embed the checked-in artifacts. Maintenance needs Zig0.15.2
(the same pin as the native CI toolchain); no tool installation is performed.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'third_party/audit-plugin'
OUTPUT = ROOT / 'internal/auditplugin/embedded'
ZIG_VERSION = '0.15.2'
TARGETS = {'amd64': 'x86_64-linux-gnu.2.28', 'arm64': 'aarch64-linux-gnu.2.28'}


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def source_digest():
    paths = ['plugin.cpp', 'snapshot.hpp', 'exports.map', 'LICENSE', 'include/pins.json',
             'include/plugin/plugin_api.h', 'include/plugin/plugin_types.h']
    return sha((''.join(f'{name}\t{sha((SOURCE / name).read_bytes())}\n' for name in sorted(paths)) +
                f'build_recipe\t{sha(Path(__file__).read_bytes())}\n').encode())



LICENSES = [('yoloctl', 'LICENSE'), ('Falco C ABI', 'third_party/audit-plugin/LICENSE')]
LICENSES += [(name, f'third_party/audit-plugin/runtime-licenses/{name}.txt')
             for name in ('libcxx', 'libcxxabi', 'libunwind', 'zig')]


def notices():
    return b''.join(f'\n==== {name} ====\n'.encode() + (ROOT / path).read_bytes()
                    for name, path in LICENSES)


def build(zig, destination):
    destination.mkdir(parents=True, exist_ok=True)
    artifacts = {}
    for arch, triple in TARGETS.items():
        output = destination / f'libyolo-linux-{arch}.so'
        command = [zig, 'c++', '-target', triple, '-std=c++17', '-O2', '-g0', '-s',
                   '-shared', '-fPIC', '-fvisibility=hidden', '-fno-ident', '-Wall', '-Wextra', '-Werror',
                   '-Wl,--build-id=none', f'-Wl,--version-script={SOURCE / "exports.map"}', '-Wl,-z,relro,-z,now',
                   '-Wl,-soname,libyolo.so', f'-ffile-prefix-map={ROOT}=/src',
                   '-I', str(SOURCE / 'include'), str(SOURCE / 'plugin.cpp'), '-o', str(output)]
        subprocess.run(command, check=True, env={**os.environ, 'SOURCE_DATE_EPOCH': '0',
                   'ZIG_LOCAL_CACHE_DIR': str(destination / 'local-cache'),
                   'ZIG_GLOBAL_CACHE_DIR': str(destination / 'global-cache')})
        raw = output.read_bytes()
        artifacts[arch] = {'file': output.name, 'sha256': sha(raw), 'bytes': len(raw)}
    return {'schema': 1, 'name': 'yolo', 'version': '1.0.0', 'api': '3.12.0',
            'sdk': 'falcosecurity/libs@0.26.1', 'zig': ZIG_VERSION,
            'source_sha256': source_digest(), 'notices_sha256': sha(notices()), 'artifacts': artifacts}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--zig', default=os.environ.get('ZIG', 'zig'))
    parser.add_argument('--check', action='store_true', help='two builds must match the tracked bytes')
    args = parser.parse_args()
    version = subprocess.check_output([args.zig, 'version'], text=True).strip()
    if version != ZIG_VERSION:
        raise SystemExit(f'need Zig{ZIG_VERSION}, got {version}')
    pins = json.loads((SOURCE / 'include/pins.json').read_text())
    for name, expected in pins['sha256'].items():
        if sha((SOURCE / 'include/plugin' / name).read_bytes()) != expected:
            raise SystemExit(f'pinned ABI header changed: {name}')
    with tempfile.TemporaryDirectory(prefix='yolo-audit-plugin-') as temporary:
        root = Path(temporary)
        first = build(args.zig, root / 'first')
        if args.check:
            second = build(args.zig, root / 'second')
            if first != second or first != json.loads((OUTPUT / 'manifest.json').read_text()):
                raise SystemExit('plugin manifest/reproducibility mismatch; explicitly regenerate')
            if (OUTPUT / 'NOTICES.txt').read_bytes() != notices():
                raise SystemExit('embedded plugin notices differ from pinned runtime licenses')
            for entry in first['artifacts'].values():
                name = entry['file']
                if (OUTPUT / name).read_bytes() != (root / 'first' / name).read_bytes():
                    raise SystemExit(f'plugin differs: {name}')
        else:
            OUTPUT.mkdir(parents=True, exist_ok=True)
            for entry in first['artifacts'].values():
                name = entry['file']
                (OUTPUT / name).write_bytes((root / 'first' / name).read_bytes())
            (OUTPUT / 'manifest.json').write_text(json.dumps(first, indent=2) + '\n')
            (OUTPUT / 'NOTICES.txt').write_bytes(notices())
        if platform.system() == 'Linux' and (SOURCE / 'test_plugin.cpp').exists():
            native = root / 'test-plugin'
            triple = TARGETS['arm64' if platform.machine() in ('aarch64', 'arm64') else 'amd64']
            subprocess.run([args.zig, 'c++', '-target', triple, '-std=c++17', '-O1', '-I',
                            str(SOURCE / 'include'), str(SOURCE / 'test_plugin.cpp'), '-o', str(native)], check=True)
            subprocess.run([str(native), str(SOURCE / "testdata/snapshot.tsv")], check=True)
        print(json.dumps({'reproducible': args.check, **first}, indent=2))


if __name__ == '__main__':
    main()
