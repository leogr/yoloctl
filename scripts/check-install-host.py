#!/usr/bin/env python3
"""Ordinary-host install acceptance. Production container refusals stay enabled."""
import argparse
import hashlib
import json
import os
import shutil
from pathlib import Path
import subprocess
import tarfile


def verify(root):
    manifest = json.loads((root / 'MANIFEST.json').read_text())
    for name, expected in manifest['files'].items():
        assert hashlib.sha256((root / name).read_bytes()).hexdigest() == expected
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('first', 'same', 'next', 'output'):
        parser.add_argument('--' + name, required=True, type=Path)
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(mode=0o700)
    env = dict(os.environ, HOME=str(output / 'home'), YOLOCTL_HOME=str(output / 'state'))
    bindir = output / 'bin'
    selected = bindir / 'yoloctl'
    keys = []
    commands = []
    for index, archive in enumerate((args.first, args.same, args.next)):
        source = output / f'source-{index}'
        source.mkdir()
        with tarfile.open(archive) as tar:
            tar.extractall(source, filter='data')
        source = source / archive.name.removesuffix('.tar.gz')
        manifest = verify(source)
        command = [str(source / 'yoloctl'), 'install', '--bin-dir', str(bindir)]
        if index == 0:
            bad = output / 'bad-source'
            bad.mkdir()
            shutil.copy2(source / 'yoloctl', bad / 'yoloctl')
            rejected = subprocess.run([str(bad / 'yoloctl'), 'install', '--dry-run', '--bin-dir', str(bindir)], env=env, capture_output=True, text=True)
            assert rejected.returncode != 0 and 'bundle verification' in rejected.stderr, rejected
            assert not bindir.exists() and not (output / 'home').exists()
            before = subprocess.run(command + ['--dry-run'], env=env, capture_output=True, text=True, check=True)
            assert not bindir.exists() and not (output / 'home').exists() and not (output / 'state').exists(), before
        for attempt in range(2 if index == 0 else 1):
            result = subprocess.run(command + ['--yes'], env=env, capture_output=True, text=True, check=True)
            commands.append({'argv': command + ['--yes'], 'exit': result.returncode, 'stdout': result.stdout, 'stderr': result.stderr})
            installed = verify(selected.resolve().parent)
            assert installed == manifest
            if attempt == 1:
                assert 'Already installed' in result.stdout
        key = selected.resolve().parent
        keys.append(key)
        assert all(path.exists() for path in keys)
        assert not (output / 'state').exists(), 'empty-state inspection wrote a home'
    assert len(set(keys)) == 3, 'same-version distinct bundle and next version need distinct immutable paths'
    (output / 'report.json').write_text(json.dumps(commands, indent=2) + '\n')
    print('PASS: host install, verified no-op, same-version distinct bundle, version upgrade; no VM migration')


if __name__ == '__main__':
    main()
