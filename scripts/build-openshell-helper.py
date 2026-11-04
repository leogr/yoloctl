#!/usr/bin/env python3
"""Explicit paired static-helper release build; never called by normal builds.

The run request must freeze the source identity, Go version and complete local
toolchain tree digest first. Build outputs remain private until review and human
publication. No VM/container operation, signing or publishing is implemented.
"""
import argparse
import gzip
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent
MEMBER = 'yoloctl-openshell-helper'


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def toolchain_digest(root):
    digest = hashlib.sha256()
    digest.update(b'yoloctl-helper-go-toolchain-v1\n')
    for file in sorted(root.rglob('*')):
        if file.is_symlink():
            raise ValueError('toolchain tree contains a symlink; freeze a complete canonical toolchain')
        if not file.is_file():
            continue
        name = file.relative_to(root).as_posix()
        size = file.stat().st_size
        digest.update(f'{len(name)}:{name}:{size}:'.encode())
        with file.open('rb') as source:
            while block := source.read(1 << 20):
                digest.update(block)
    return digest.hexdigest()


def go_run(root, arguments, log=None, extra=None):
    env = dict(os.environ)
    env.update(GOFLAGS='', GOENV='off', GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='0', GOMAXPROCS='2')
    for key in ('GOOS', 'GOARCH', 'GOEXPERIMENT', 'GOAMD64', 'GOARM64', 'GOFIPS140', 'GODEBUG'):
        env.pop(key, None)
    if extra:
        env.update(extra)
    result = subprocess.run(['go', *arguments], cwd=root, env=env, capture_output=True, timeout=900)
    if log is not None:
        log.write_bytes(result.stdout + result.stderr)
        log.with_suffix(log.suffix + '.exit').write_text(str(result.returncode) + '\n')
    if result.returncode:
        raise ValueError(f'Go helper build/check failed ({result.returncode}); inspect {log or "the command output"}')
    return result.stdout.decode()


def archive(binary, destination):
    raw = binary.read_bytes()
    with destination.open('xb') as target:
        with gzip.GzipFile(filename='', fileobj=target, mode='wb', mtime=0) as compressed:
            with tarfile.open(fileobj=compressed, mode='w', format=tarfile.USTAR_FORMAT) as output:
                entry = tarfile.TarInfo(MEMBER)
                entry.mode, entry.uid, entry.gid, entry.size, entry.mtime = 0o755, 0, 0, len(raw), 0
                output.addfile(entry, io.BytesIO(raw))
    return {'binary_sha256': sha(raw), 'archive_sha256': sha(destination.read_bytes())}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True, help='new evidence/output directory')
    parser.add_argument('--native-root', type=Path, required=True, help='verified VM/native filesystem for fresh build caches')
    parser.add_argument('--go-version', required=True)
    parser.add_argument('--toolchain-sha256', required=True)
    parser.add_argument('--source-sha256', required=True)
    args = parser.parse_args()
    if not re.fullmatch(r'go[0-9]+\.[0-9]+\.[0-9]+', args.go_version) or not all(re.fullmatch('[0-9a-f]{64}', value) for value in (args.toolchain_sha256, args.source_sha256)):
        parser.error('freeze an exact Go version, toolchain digest and source digest before execution')
    output = args.out.absolute()
    owned_output = False
    cache_used = False
    failure = None
    completed = False
    failure_exit = 1
    try:
        native = args.native_root.resolve(strict=True)
        if not native.is_dir() or output.exists() or output.is_symlink():
            raise ValueError('native root must exist and output must be a new path')
        if shutil.disk_usage(native).free < 4 << 30:
            raise ValueError('native build cache filesystem needs at least 4 GiB free')
        version = go_run(ROOT, ['env', 'GOVERSION']).strip()
        goroot = Path(go_run(ROOT, ['env', 'GOROOT']).strip()).resolve(strict=True)
        if version != args.go_version or toolchain_digest(goroot) != args.toolchain_sha256:
            raise ValueError('Go toolchain differs from frozen request')
        # Download/verify through the module checksum mechanism before reading
        # embedded source bytes. Never permit an unchecked local replacement.
        go_run(ROOT, ['mod', 'verify'])
        cache_used = True
        baseline = json.loads(go_run(ROOT, ['run', '-buildvcs=false', './scripts/openshell-helper-inputs.go']))
        if baseline['source_sha256'] != args.source_sha256:
            raise ValueError('compiled helper production inputs differ from frozen request')
        output.mkdir(mode=0o700)
        owned_output = True
        spec = importlib.util.spec_from_file_location('helper_input_check', ROOT / 'scripts/check-helper-inputs.py')
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        source_proof = checker.check(ROOT, mutations=True)
        source_proof.update(go_version=version, toolchain_sha256=args.toolchain_sha256,
                            recipe_sha256=sha((ROOT / 'scripts/openshell-helper-recipe.json').read_bytes()),
                            builder_sha256=sha(Path(__file__).read_bytes()),
                            checker_sha256=sha((ROOT / 'scripts/check-helper-inputs.py').read_bytes()),
                            inspector_sha256=sha((ROOT / 'scripts/openshell-helper-inputs.go').read_bytes()))
        (output / 'source-proof.json').write_text(json.dumps(source_proof, indent=2, sort_keys=True) + '\n')
        for arch in ('amd64', 'arm64'):
            builds = []
            reports = []
            for number in (1, 2):
                if shutil.disk_usage(native).free < 4 << 30:
                    raise ValueError("native helper build headroom fell below 4 GiB")
                with tempfile.TemporaryDirectory(prefix=f'yoloctl-helper-{arch}-{number}-', dir=native) as temporary:
                    temporary = Path(temporary)
                    cache = temporary / 'cache'
                    cache.mkdir(mode=0o700)
                    binary = temporary / MEMBER
                    go_run(ROOT, ['build', '-buildvcs=false', '-trimpath', '-ldflags=-s -w -buildid=', '-o', str(binary), './cmd/yoloctl-openshell-helper'],
                           output / f'{arch}-build-{number}.log', {'GOOS': 'linux', 'GOARCH': arch, 'GOCACHE': str(cache)})
                    report = json.loads(go_run(ROOT, ['run', '-buildvcs=false', './scripts/openshell-helper-inputs.go', '--binary', str(binary), '--arch', arch],
                                               output / f'{arch}-inspect-{number}.log'))
                    if report['inputs'] != baseline:
                        raise ValueError('helper input identity changed during release build')
                    destination = output / f'{MEMBER}-linux-{arch}-build-{number}.tar.gz'
                    builds.append(archive(binary, destination))
                    reports.append(report)
            if builds[0] != builds[1] or reports[0] != reports[1]:
                raise ValueError(f'{arch}: two clean helper builds did not reproduce identical bytes/producer metadata')
            proof = {'architecture': arch, 'go_version': version, 'toolchain_sha256': args.toolchain_sha256,
                     'inputs': baseline, 'builds': builds, 'producer_reports': reports,
                     'source_proof_sha256': sha((output / 'source-proof.json').read_bytes())}
            (output / f'build-proof-{arch}.json').write_text(json.dumps(proof, indent=2, sort_keys=True) + '\n')
        final = json.loads(go_run(ROOT, ['run', '-buildvcs=false', './scripts/openshell-helper-inputs.go']))
        if final != baseline or toolchain_digest(goroot) != args.toolchain_sha256:
            raise ValueError('source/toolchain changed during paired release build')
        completed = True
    except BaseException as error:
        # KeyboardInterrupt/SystemExit and unexpected failures must not pass
        # through finally with an unset failure and fabricate runner.exit=0.
        failure = f'{type(error).__name__}: {error}'
        failure_exit = 130 if isinstance(error, KeyboardInterrupt) else 1
    finally:
        # This explicit release gate owns its normal Go compiler caches too.
        # Fresh native build directories above are exact tempfile-owned paths.
        if cache_used:
            try:
                go_run(ROOT, ['clean', '-cache', '-testcache'], output / 'cache-clean.log' if owned_output else None)
            except BaseException as error:
                failure = f'{failure or "build completed"}; cache cleanup failed: {error}'
                if isinstance(error, KeyboardInterrupt):
                    failure_exit = 130
        if owned_output:
            status = 0 if completed and failure is None else failure_exit
            (output / 'runner.exit').write_text(str(status) + '\n')
    if not completed or failure is not None:
        parser.exit(failure_exit, f'OpenShell helper release FAILED: {failure or "checks incomplete"}\n')
    print(f'Paired static helper artifacts verified in {output}; review and human publication still required')


if __name__ == '__main__':
    main()
