#!/usr/bin/env python3
"""Build the OpenShell sandbox/supervisor archives for amd64 and arm64 from upstream + our patches.

Inputs are third_party/openshell/pins.json and its patches. Their hash (with this script) keys a
cache directory bin/openshell/<key>/ holding the four archives (the layout `make native-qualify`
reads as NATIVE_ARCHIVES), sandbox-policy.rego, the build's own qualification inputs and
RECEIPT.json. A cache hit re-verifies the stored archives against the receipt; otherwise both
static musl targets are built on the host (Linux or macOS) with cargo-zigbuild:

  rustup toolchain from pins.json with the x86_64/aarch64 unknown-linux-musl targets, zig, cargo-zigbuild, git.

The source chain (upstream tree, each patch result tree, stamped Cargo files) must match the pins.
The outputs are used as built; the receipt records the toolchain and whether they happen to equal
the reviewed release hashes in pins.json. Pins are never rewritten by this script.

usage: openshell-build.py key|path|check|build [--cache DIR]
"""
import argparse
import gzip
import hashlib
import io
import json
import re
import os
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
PINS_DIR = ROOT / 'third_party' / 'openshell'
DEFAULT_CACHE = ROOT / 'bin' / 'openshell'
BINARIES = ('openshell-sandbox', 'openshell-supervisor')
ARCHES = ('amd64', 'arm64')
ROLES = {'boundary': 'openshell-sandbox', 'supervisor': 'openshell-supervisor'}
SETUP = ('install rustup (https://rustup.rs) and zig (https://ziglang.org, e.g. `brew install zig`), then run '
         '`rustup toolchain install {rust} --target x86_64-unknown-linux-musl,aarch64-unknown-linux-musl` '
         'and `cargo install --locked cargo-zigbuild`')


class BuildError(Exception):
    pass


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, 'rb') as f:
        while chunk := f.read(1 << 20):
            h.update(chunk)
    return h.hexdigest()


def load_pins(pins_dir=PINS_DIR):
    return json.loads((pins_dir / 'pins.json').read_text())


def input_files(pins, pins_dir=PINS_DIR):
    """Every file whose bytes decide the build."""
    return [pins_dir / 'pins.json', Path(__file__).resolve()] + [pins_dir / p['file'] for p in pins['patches']]


def cache_key(pins, pins_dir=PINS_DIR):
    lines = ['%s  %s\n' % (sha256_file(path), path.name) for path in input_files(pins, pins_dir)]
    return sha256_bytes(''.join(sorted(lines)).encode())[:24]


def check(pins, pins_dir=PINS_DIR):
    """Offline consistency of the pins with the committed files (no network, no build)."""
    errors = []
    for p in pins['patches']:
        if sha256_file(pins_dir / p['file']) != p['sha256']:
            errors.append('patch %s hash differs from pins' % p['file'])
    if sha256_file(pins_dir / pins['rego']['file']) != pins['rego']['sha256']:
        errors.append('committed Rego differs from pins')
    release = pins_dir / 'release-inputs.json'
    if release.is_file():
        inputs = json.loads(release.read_text())
        for arch, by_role in inputs.get('platforms', {}).items():
            for role, item in by_role.items():
                if item['archive'] != '%s-%s.tar.gz' % (ROLES[role], arch):
                    errors.append('release-inputs %s/%s archive name differs' % (arch, role))
    for arch in ARCHES:
        if set(pins['outputs'][arch]) != set(BINARIES) or not pins['targets'].get(arch):
            errors.append('outputs or target for %s incomplete' % arch)
    if errors:
        raise BuildError('; '.join(errors))


def pack(binary_path, archive_path):
    """Single-member archive: ustar, numeric root owner 0555, mtime 0 (byte-equal to GNU tar --format=ustar
    --numeric-owner), compressed by `gzip -n` when present (GNU gzip reproduces the reviewed archives) else zlib."""
    data = binary_path.read_bytes()
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode='w', format=tarfile.USTAR_FORMAT) as t:
        info = tarfile.TarInfo(binary_path.name)
        info.size, info.mode, info.mtime, info.uid, info.gid, info.uname, info.gname = len(data), 0o555, 0, 0, 0, '', ''
        t.addfile(info, io.BytesIO(data))
    if shutil.which('gzip'):
        compressed = subprocess.run(['gzip', '-n'], input=buf.getvalue(), check=True, stdout=subprocess.PIPE).stdout
    else:
        compressed = gzip.compress(buf.getvalue(), mtime=0)
    archive_path.write_bytes(compressed)


def archive_names():
    return ['%s-%s.tar.gz' % (binary, arch) for arch in ARCHES for binary in BINARIES]


def cached(cache, key):
    """The cache entry for key when complete and its archives still equal its receipt, else None."""
    directory = cache / key
    try:
        receipt = json.loads((directory / 'RECEIPT.json').read_text())
    except (OSError, ValueError):
        return None
    for name in archive_names() + ['sandbox-policy.rego']:
        if not (directory / name).is_file() or sha256_file(directory / name) != receipt['archives'].get(name):
            return None
    for name in ('release-inputs.json', 'source-proof.json', 'build-amd64.json', 'build-arm64.json'):
        if not (directory / name).is_file():
            return None
    return directory


def run(argv, log, **kw):
    with open(log, 'ab') as out:
        out.write(('$ %s\n' % ' '.join(argv)).encode())
        out.flush()
        status = subprocess.run(argv, stdout=out, stderr=subprocess.STDOUT, **kw).returncode
    if status:
        raise BuildError('%s exited %d (log %s)' % (' '.join(argv[:2]), status, log))


def output(argv, env=None):
    return subprocess.run(argv, env=env, check=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True).stdout.strip()


def toolchain(pins):
    """The cargo environment, after checking every host dependency; raises with the setup line when one is missing."""
    setup = SETUP.format(rust=pins['rust'])
    for tool in ('git', 'rustup', 'cargo', 'zig', 'cargo-zigbuild'):
        if shutil.which(tool) is None:
            raise BuildError('%s not found: %s' % (tool, setup))
    env = dict(os.environ, RUSTUP_TOOLCHAIN=pins['rust'], CARGO_INCREMENTAL='0', CARGO_PROFILE_RELEASE_DEBUG='0',
               SOURCE_DATE_EPOCH=str(pins['source_date_epoch']), LC_ALL='C', GIT_DIR='/nonexistent')
    try:
        installed = output(['rustup', 'target', 'list', '--installed'], env).split()
        versions = {'rustc': output(['rustc', '-V'], env), 'cargo': output(['cargo', '-V'], env),
                    'zig': output(['zig', 'version'], env), 'cargo-zigbuild': output(['cargo-zigbuild', '--version'], env)}
    except subprocess.CalledProcessError as e:
        raise BuildError('Rust %s toolchain unavailable (%s): %s' % (pins['rust'], e.output.strip()[:200], setup))
    missing = [pins['targets'][arch] for arch in ARCHES if pins['targets'][arch] not in installed]
    if missing:
        raise BuildError('missing Rust targets %s: %s' % (', '.join(missing), setup))
    return env, versions


def prepare_source(pins, pins_dir, work, log):
    """Fetch the pinned upstream commit, apply the patches in order, verify every tree and stamp the version."""
    git = work / 'git'
    git.mkdir()
    env = dict(os.environ, GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull)
    g = lambda *a: run(['git', '-C', str(git)] + list(a), log, env=env)
    g('init', '-q')
    g('fetch', '-q', '--depth=1', pins['upstream']['repository'], pins['upstream']['commit'])
    g('read-tree', pins['upstream']['commit'])
    trees = [pins['upstream']['tree']] + [p['result_tree'] for p in pins['patches']]
    patches = [None] + pins['patches']
    for want, patch in zip(trees, patches):
        if patch is not None:
            g('apply', '--cached', str(pins_dir / patch['file']))
        got = output(['git', '-C', str(git), 'write-tree'], env)
        if got != want:
            raise BuildError('source tree %s does not match the pinned source (%s)' % (got, want))
    source = work / pins['source_dir']
    g('checkout-index', '-a', '--prefix=%s/' % source)
    for name in ('Cargo.toml', 'Cargo.lock'):
        f = source / name
        if sha256_file(f) != pins['stamp']['before'][name]:
            raise BuildError('%s before stamping does not match the pinned source' % name)
        f.write_bytes(f.read_bytes().replace(pins['stamp']['from'].encode(), pins['stamp']['to'].encode()))
        if sha256_file(f) != pins['stamp']['after'][name]:
            raise BuildError('%s after stamping does not match the pinned source' % name)
    if sha256_file(source / pins['rego']['path']) != pins['rego']['sha256']:
        raise BuildError('source Rego differs from the pinned Rego')
    return source


def compile_all(pins, env, source, work, out, log):
    """cargo zigbuild of both binaries for each musl target; paths under the work root are mapped to /build."""
    env = dict(env, CARGO_TARGET_DIR=str(work / 'target'), RUSTFLAGS='--remap-path-prefix=%s=/build' % work)
    binaries = {}
    for arch in ARCHES:
        triple = pins['targets'][arch]
        binaries[arch] = {}
        for binary in BINARIES:
            run(['cargo', 'zigbuild', '--release', '--locked', '--target', triple, '-p', binary, '--bin', binary],
                log, cwd=source, env=env)
            built = work / 'target' / triple / 'release' / binary
            pack(built, out / ('%s-%s.tar.gz' % (binary, arch)))
            binaries[arch][binary] = sha256_file(built)
    shutil.copyfile(source / pins['rego']['path'], out / 'sandbox-policy.rego')
    return binaries


def write_qualify_inputs(pins, pins_dir, key, out, binaries, versions):
    """Evidence for `make native-qualify` from this build alone: what this run verified, nothing more.

    source-proof.json: the upstream fetch and ordered patch trees this run checked (also the patch-2 record);
    build-<arch>.json: toolchain and the hashes this single local build produced;
    release-inputs.json: the committed release inputs with these outputs and evidence paths.
    No CI, signature or two-build reproduction is claimed.
    """
    def dump(name, value):
        (out / name).write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')
    dump('source-proof.json', {
        'mode': 'local openshell-build.py source step: fetched upstream commit, applied the pinned patches in order, '
                'verified every resulting Git tree and the stamped Cargo files',
        'cache_key': key, 'upstream': pins['upstream'],
        'patches': [{'name': Path(p['file']).name, 'sha256': p['sha256'], 'tree': p['result_tree']} for p in pins['patches']],
        'stamp_after': pins['stamp']['after'], 'claims_not_made': ['signature', 'upstream CI', 'two-build reproduction']})
    inputs = json.loads((pins_dir / 'release-inputs.json').read_text())
    for arch in ARCHES:
        outputs = {}
        for role, binary in ROLES.items():
            name = '%s-%s.tar.gz' % (binary, arch)
            outputs[name] = {'archive_sha256': sha256_file(out / name), 'binary_sha256': binaries[arch][binary]}
            inputs['platforms'][arch][role].update(archive=name, **outputs[name])
        dump('build-%s.json' % arch, {
            'mode': 'single local cargo-zigbuild build by openshell-build.py', 'cache_key': key,
            'target': pins['targets'][arch], 'toolchain': versions, 'host': '%s/%s' % (platform.system(), platform.machine()),
            'source_date_epoch': pins['source_date_epoch'], 'outputs': outputs})
    # The built source is the last pinned patch tree. Patches after the second
    # are chained by name, hash and tree; their hashes are in source-proof.json.
    patches = pins['patches']
    inputs.update(revision='local-' + key, patch2_record='source-proof.json', source_proof='source-proof.json',
                  build_proofs={arch: 'build-%s.json' % arch for arch in ARCHES},
                  patch2_sha256=patches[1]['sha256'], patch2_tree=patches[1]['result_tree'],
                  fork_tree=patches[-1]['result_tree'],
                  extra_patches=[{'name': re.sub(r'^\d+-', '', Path(p['file']).name), 'sha256': p['sha256'],
                                  'tree': p['result_tree']} for p in patches[2:]])
    dump('release-inputs.json', inputs)


def build(pins, cache, pins_dir=PINS_DIR):
    key = cache_key(pins, pins_dir)
    hit = cached(cache, key)
    if hit:
        return hit, True
    check(pins, pins_dir)
    env, versions = toolchain(pins)
    cache.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix='work-%s-' % key, dir=cache))
    log = work / 'build.log'
    out = work / 'out'
    out.mkdir()
    print('openshell-build: building OpenShell %s for amd64 and arm64 (first build takes a while; log %s)'
          % (pins['version'], log), file=sys.stderr, flush=True)
    source = prepare_source(pins, pins_dir, work, log)
    binaries = compile_all(pins, env, source, work, out, log)
    write_qualify_inputs(pins, pins_dir, key, out, binaries, versions)
    archives = {f.name: sha256_file(f) for f in sorted(out.iterdir()) if f.name.endswith(('.tar.gz', '.rego'))}
    reviewed = all(archives['%s-%s.tar.gz' % (b, a)] == pins['outputs'][a][b]['archive_sha256'] for a in ARCHES for b in BINARIES)
    receipt = {'key': key, 'inputs': {p.name: sha256_file(p) for p in input_files(pins, pins_dir)},
               'upstream': pins['upstream'], 'source_tree': pins['patches'][-1]['result_tree'], 'toolchain': versions,
               'host': '%s/%s' % (platform.system(), platform.machine()), 'archives': archives,
               'binaries': binaries, 'equal_to_reviewed_release': reviewed}
    (out / 'RECEIPT.json').write_text(json.dumps(receipt, indent=2, sort_keys=True) + '\n')
    shutil.copyfile(log, out / 'build.log')
    final = cache / key
    os.replace(out, final)
    shutil.rmtree(work, ignore_errors=True)
    return final, False


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('command', choices=('key', 'path', 'check', 'build'))
    parser.add_argument('--cache', type=Path, default=DEFAULT_CACHE)
    args = parser.parse_args(argv)
    pins = load_pins()
    try:
        if args.command == 'key':
            print(cache_key(pins))
        elif args.command == 'path':
            print(args.cache.resolve() / cache_key(pins))
        elif args.command == 'check':
            check(pins)
            print('pins consistent')
        else:
            directory, hit = build(pins, args.cache.resolve())
            print('%s %s' % ('cached' if hit else 'built', directory))
    except BuildError as e:
        print('openshell-build: %s' % e, file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    sys.exit(main())
