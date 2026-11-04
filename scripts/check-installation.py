#!/usr/bin/env python3
"""Read-only executable acceptance. Runs no VM or live installation."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def check_roots(binary, channel):
    with tempfile.TemporaryDirectory(prefix='yir-') as tmp:
        root = Path(tmp)
        env = dict(os.environ, XDG_CONFIG_HOME=str(root / 'config'),
                   XDG_CACHE_HOME=str(root / 'cache'), YOLOCTL_HOME='')
        leaf = 'yoloctl'
        for home in ('', str(root / 'override'), 'relative'):
            env['YOLOCTL_HOME'] = home
            for args in (['version'], ['version', '--json']):
                result = subprocess.run([binary, *args], env=env, capture_output=True,
                                        text=True, timeout=5)
                if home == 'relative':
                    assert result.returncode != 0 and 'YOLOCTL_HOME' in result.stderr, result
                else:
                    assert result.returncode == 0, result
                    if '--json' in args:
                        info = json.loads(result.stdout)
                        assert info['channel'] == channel, info
                        assert info['config_root'] == (home or str(root / 'config' / leaf)), info
                        assert info['cache_root'] == (str(Path(home) / 'cache') if home else str(root / 'cache' / leaf)), info
                assert list(root.iterdir()) == [], list(root.iterdir())
        # A hostile Lima probe must finish within its own deadline, including
        # descendants holding stdout/stderr open.
        tools = root / 'tools'
        tools.mkdir()
        probe = tools / 'limactl'
        probe.write_text('#!/bin/sh\nsleep 30\n')
        probe.chmod(0o700)
        env.update(PATH=str(tools) + os.pathsep + env['PATH'], YOLOCTL_HOME='')
        begin = time.monotonic()
        subprocess.run([binary, 'version', '--json'], env=env, check=True,
                       capture_output=True, timeout=4)
        assert time.monotonic() - begin < 4
        assert list(root.iterdir()) == [tools]
    print(f'PASS: {channel} executable roots and bounded probe')


def check_refusals(dev, release):
    with tempfile.TemporaryDirectory(prefix='yif-') as tmp:
        root = Path(tmp)
        env = dict(os.environ, HOME=str(root / 'home'), YOLOCTL_HOME=str(root / 'state'),
                   XDG_CONFIG_HOME=str(root / 'config'), XDG_CACHE_HOME=str(root / 'cache'))
        for binary in (dev, release):
            for flags in ([], ['--dry-run'], ['--yes']):
                result = subprocess.run([binary, 'install', '--bin-dir', str(root / 'bin'), *flags],
                                        env=env, capture_output=True, text=True, timeout=5)
                assert result.returncode != 0, result
                assert list(root.iterdir()) == [], list(root.iterdir())
        print('PASS: executable dev/container install refusals; all roots and bin untouched')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dev', required=True)
    parser.add_argument('--release', required=True)
    args = parser.parse_args()
    check_roots(str(Path(args.dev).resolve(strict=True)), 'dev')
    check_roots(str(Path(args.release).resolve(strict=True)), 'release')

    check_refusals(str(Path(args.dev).resolve(strict=True)), str(Path(args.release).resolve(strict=True)))
