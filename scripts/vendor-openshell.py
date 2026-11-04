#!/usr/bin/env python3
"""Check the reviewed OpenShell source/patch/artifact qualification, read-only.

This command never builds Rust, updates pins, regenerates qualification, signs a
commit or publishes anything. Source archives can be supplied offline; otherwise
only the exact hash-pinned source URLs in the reviewed proof are downloaded.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
PIN_FIELDS = ('version', 'upstream_commit', 'upstream_tree', 'fork_commit', 'fork_tree',
              'patches', 'platforms', 'rego_sha256', 'bootstrap_schema',
              'protocol_fixture_version', 'protocol_fixture_sha256', 'helper')
PATCH_FIELDS = ('name', 'sha256', 'parent_tree', 'tree')
PLATFORM_FIELDS = ('boundary', 'supervisor', 'boundary_loader', 'supervisor_loader')
ARTIFACT_FIELDS = ('url', 'archive_sha256', 'binary_sha256', 'member')
QUALIFICATION_FIELDS = ('revision', 'subject_sha256', 'source_proof_sha256', 'build_proof_sha256')
HELPER_FIELDS = ('protocol', 'inputs', 'go_version', 'source_proof_sha256', 'build_proof_sha256', 'platforms')
HELPER_INPUT_FIELDS = ('schema', 'source_sha256', 'modules')
MODULE_FIELDS = ('path', 'version', 'sum')


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def read_regular(p, limit=1 << 20):
    fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as f:
        import stat
        require(stat.S_ISREG(os.fstat(f.fileno()).st_mode), f'not a regular file: {p}')
        raw = f.read(limit + 1)
    require(len(raw) <= limit, f'file exceeds bounds: {p}')
    return raw


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f'duplicate JSON field: {key}')
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique_object)


def fields(value, expected):
    require(isinstance(value, dict) and set(value) == set(expected), 'missing/unknown qualification fields')
    return {name: value[name] for name in expected}


def subject_bytes(pins):
    """The exact ASCII encoding of Go json.Marshal(ReleasePins), map keys sorted."""
    pins = fields(pins, PIN_FIELDS)
    require(isinstance(pins['patches'], list), 'patches must be an ordered array')
    pins['patches'] = [fields(p, PATCH_FIELDS) for p in pins['patches']]
    require(isinstance(pins['platforms'], dict), 'platforms must be an object')
    platforms = {}
    for arch, original in sorted(pins['platforms'].items()):
        value = fields(original, PLATFORM_FIELDS)
        for role in ('boundary', 'supervisor'):
            value[role] = fields(value[role], ARTIFACT_FIELDS)
        platforms[arch] = value
    pins['platforms'] = platforms
    helper = fields(pins['helper'], HELPER_FIELDS)
    helper['inputs'] = fields(helper['inputs'], HELPER_INPUT_FIELDS)
    helper['inputs']['modules'] = [fields(m, MODULE_FIELDS) for m in helper['inputs']['modules']]
    helper['platforms'] = {arch: fields(a, ARTIFACT_FIELDS) for arch, a in sorted(helper['platforms'].items())}
    helper['build_proof_sha256'] = dict(sorted(helper['build_proof_sha256'].items()))
    pins['helper'] = helper
    # Pin strings are deliberately bounded ASCII; Go's HTML escaping is also
    # preserved so this function cannot compute a different reviewed subject.
    encoded = json.dumps(pins, separators=(',', ':'), ensure_ascii=False)
    encoded = encoded.replace('&', r'\u0026').replace('<', r'\u003c').replace('>', r'\u003e')
    encoded = encoded.replace('\u2028', r'\u2028').replace('\u2029', r'\u2029')
    return encoded.encode()


EMBEDDED_ARCHIVE = re.compile(r'embedded:[a-z0-9][a-z0-9._-]{0,126}\.tar\.gz')


def embedded_archive(location):
    """A qualified artifact location: a logical embedded archive name, never a
    network address (mirrors internal/openshell embeddedArchiveName)."""
    return isinstance(location, str) and EMBEDDED_ARCHIVE.fullmatch(location) is not None


def hex_string(value, digits):
    return isinstance(value, str) and re.fullmatch('[0-9a-f]{' + str(digits) + '}', value) is not None


def go_constant(root, name, filename='internal/openshell/release.go'):
    source = read_regular(root / filename).decode()
    match = re.search(r'\b' + re.escape(name) + r'\s*=\s*"([^"]+)"', source)
    require(match is not None, f'missing mirrored Go constant: {name}')
    return match[1]


def validate_helper(value, root):
    p = fields(value, HELPER_FIELDS)
    identity = fields(p['inputs'], HELPER_INPUT_FIELDS)
    recipe = decode(read_regular(root / 'scripts/openshell-helper-recipe.json'))
    require(p['protocol'] == go_constant(root, 'HelperProtocol', 'internal/openshell/helper.go'), 'helper protocol differs')
    require(type(identity['schema']) is int and identity['schema'] == recipe['schema'] == 1 and hex_string(identity['source_sha256'], 64), 'missing helper source identity')
    modules = [fields(m, MODULE_FIELDS) for m in identity['modules']]
    require([m['path'] for m in modules] == recipe['modules'], 'helper dependency inventory differs')
    require(all(isinstance(m['version'], str) and m['version'].startswith('v') and isinstance(m['sum'], str) and m['sum'].startswith('h1:') for m in modules), 'unqualified helper modules')
    require(re.fullmatch(r'go[0-9]+\.[0-9]+\.[0-9]+', p['go_version']) and hex_string(p['source_proof_sha256'], 64), 'missing helper producer/source proof')
    require(set(p['platforms']) == set(p['build_proof_sha256']) == {'amd64', 'arm64'}, 'both helper platforms must be qualified')
    for arch, original in p['platforms'].items():
        a = fields(original, ARTIFACT_FIELDS)
        require(embedded_archive(a['url']), 'helper is not an embedded release archive')
        require(a['member'] == go_constant(root, 'HelperMember', 'internal/openshell/helper.go') and
                hex_string(a['archive_sha256'], 64) and hex_string(a['binary_sha256'], 64) and
                hex_string(p['build_proof_sha256'][arch], 64), 'incomplete static helper pins/proof')
    require(p['platforms']['amd64']['url'] != p['platforms']['arm64']['url'], 'mixed helper architecture assets')


def validate_document(document, root=ROOT):
    fields(document, ('pins', 'qualification'))
    p = fields(document['pins'], PIN_FIELDS)
    q = fields(document['qualification'], QUALIFICATION_FIELDS)
    require(sha(subject_bytes(p)) == q['subject_sha256'], 'pins changed without reviewed qualification')
    require(isinstance(q['revision'], str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,62}', q['revision']), 'invalid qualification revision')
    for field, constant in (('version', 'qualifiedVersion'), ('upstream_commit', 'qualifiedUpstreamCommit'),
                            ('upstream_tree', 'qualifiedUpstreamTree'), ('protocol_fixture_version', 'protocolFixtureVersion')):
        require(p[field] == go_constant(root, constant), f'unqualified {field} / Go mirror mismatch')
    require(type(p['bootstrap_schema']) is int and p['bootstrap_schema'] == 2, 'unsupported bootstrap schema')
    source = read_regular(root / 'internal/openshell/bootstrap.go').decode()
    require(re.search(r'\bBootstrapSchema\s*=\s*2\b', source), 'Go bootstrap schema differs')
    for field, digits in (('fork_commit', 40), ('fork_tree', 40), ('rego_sha256', 64), ('protocol_fixture_sha256', 64)):
        require(hex_string(p[field], digits), f'incomplete {field}')
    require(hex_string(q['source_proof_sha256'], 64), 'missing source qualification')
    validate_helper(p['helper'], root)
    patches = p['patches']
    require(len(patches) == 2 and [x['name'] for x in patches] == ['unmediated-network.patch', 'workspace-read-only.patch'], 'missing/reordered patches')
    require(patches[0]['sha256'] == go_constant(root, 'qualifiedPatch1SHA256') and
            patches[0]['parent_tree'] == p['upstream_tree'] and
            patches[0]['tree'] == go_constant(root, 'qualifiedPatch1Tree') and
            patches[1]['parent_tree'] == patches[0]['tree'] and patches[1]['tree'] == p['fork_tree'] and
            hex_string(patches[1]['sha256'], 64), 'source patch stack differs')
    require(set(p['platforms']) == {'amd64', 'arm64'} and set(q['build_proof_sha256']) == {'amd64', 'arm64'}, 'both platform pairs must be qualified')
    for arch, platform in p['platforms'].items():
        require(hex_string(q['build_proof_sha256'][arch], 64), 'missing platform build proof')
        require(platform['boundary_loader'] and platform['supervisor_loader'], 'missing loader qualification')
        require(platform['boundary']['url'] != platform['supervisor']['url'], 'mixed supervisor/boundary URL')
        for role, member in (('boundary', 'openshell-sandbox'), ('supervisor', 'openshell-supervisor')):
            artifact = platform[role]
            require(embedded_archive(artifact['url']), 'artifact is not an embedded release archive')
            require(artifact['member'] == member and hex_string(artifact['archive_sha256'], 64) and hex_string(artifact['binary_sha256'], 64), 'incomplete artifact pin pair')
    return p, q


def git(root, *args, body=None, raw=False):
    env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
    env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL='/dev/null', GIT_TERMINAL_PROMPT='0')
    command = ['git', '-c', 'core.autocrlf=false', '-c', 'core.fileMode=true', '-c', 'core.hooksPath=/dev/null', *args]
    result = subprocess.run(command, cwd=root, env=env, input=body, capture_output=True, timeout=90)
    require(result.returncode == 0, 'git source proof failed: ' + ' '.join(args[:2]) + ': ' + result.stderr.decode(errors='replace')[:1000])
    return result.stdout if raw else result.stdout.decode().strip()


def archive_tree(archive, repo):
    """Construct an index from blob bytes/modes/links, never extract source paths."""
    git(repo, 'init', '--quiet', '--template=/dev/null', '--object-format=sha1')
    git(repo, 'read-tree', '--empty')
    entries, top, total = {}, None, 0
    with tarfile.open(archive, mode='r|gz') as source:
        for count, member in enumerate(source):
            require(count < 10000, 'source archive has too many entries')
            p = PurePosixPath(member.name)
            require(p.parts and not p.is_absolute() and '..' not in p.parts and '.git' not in p.parts and
                    not any(ord(c) < 32 or ord(c) == 127 for c in member.name), 'unsafe source archive path')
            top = top or p.parts[0]
            require(p.parts[0] == top, 'source archive has multiple roots')
            if len(p.parts) == 1:
                require(member.isdir(), 'source archive root must be a directory')
                continue
            name = str(PurePosixPath(*p.parts[1:]))
            if member.isdir():
                continue
            require(name not in entries and (member.isfile() or member.issym()), 'duplicate or special source archive entry')
            if member.issym():
                raw, mode = member.linkname.encode(), '120000'
            else:
                require(0 <= member.size <= 64 << 20, 'source member too large')
                total += member.size
                require(total <= 300 << 20, 'source archive exceeds total bounds')
                with source.extractfile(member) as file:
                    raw = file.read(member.size + 1)
                require(len(raw) == member.size, 'truncated source member')
                mode = '100755' if member.mode & 0o100 else '100644'
            oid = git(repo, 'hash-object', '-w', '--stdin', body=raw)
            entries[name] = (mode, oid)
    require(entries, 'empty source archive')
    for name in entries:
        require(not any(str(parent) in entries for parent in PurePosixPath(name).parents), 'source path descends through file/symlink')
    records = b''.join((mode + ' ' + oid + '\t' + name).encode() + b'\0' for name, (mode, oid) in sorted(entries.items()))
    git(repo, 'update-index', '-z', '--index-info', body=records)
    return git(repo, 'write-tree')


def source_archive(entry, supplied, dest):
    fields(entry, ('commit', 'tree', 'archive_url', 'archive_sha256', 'commit_object_sha256'))
    require(hex_string(entry['commit'], 40) and hex_string(entry['tree'], 40) and hex_string(entry['archive_sha256'], 64), 'incomplete source anchor')
    parsed = urllib.parse.urlsplit(entry['archive_url'])
    require(parsed.scheme == 'https' and parsed.netloc == 'github.com' and not parsed.query and not parsed.fragment and
            parsed.path == f"/NVIDIA/OpenShell/archive/{entry['commit']}.tar.gz", 'source URL is not pinned to an exact commit')
    if supplied:
        raw = read_regular(supplied, 128 << 20)
    else:
        with urllib.request.urlopen(entry['archive_url'], timeout=60) as response:
            require(urllib.parse.urlsplit(response.url).scheme == 'https', 'source download redirected away from HTTPS')
            raw = response.read((128 << 20) + 1)
        require(len(raw) <= 128 << 20, 'source archive exceeds bounds')
    require(sha(raw) == entry['archive_sha256'], 'source archive SHA256 mismatch')
    dest.write_bytes(raw)
    return dest


class SelectionPending(Exception):
    """The build carries no production native selection yet: not a pass."""


SELECTION_FILE = 'assets/native_openshell.go'
SELECTION_NAMES = ('nativeOpenShellManifest', 'nativeOpenShellManifestSHA256', 'nativeOpenShellRego')
GO_ESCAPES = {'a': 7, 'b': 8, 'f': 12, 'n': 10, 'r': 13, 't': 9, 'v': 11, '\\': 92, '"': 34}


def go_string(literal):
    """Decode one Go string literal (raw or interpreted) to its bytes."""
    if literal.startswith('`'):
        return literal[1:-1].replace('\r', '').encode()
    body, out, i = literal[1:-1], bytearray(), 0
    while i < len(body):
        c = body[i]
        if c != '\\':
            out += c.encode()
            i += 1
            continue
        e = body[i + 1:i + 2]
        if e in GO_ESCAPES:
            out.append(GO_ESCAPES[e])
            i += 2
        elif e == 'x' and re.fullmatch('[0-9a-fA-F]{2}', body[i + 2:i + 4]):
            out.append(int(body[i + 2:i + 4], 16))
            i += 4
        elif re.fullmatch('[0-3][0-7]{2}', body[i + 1:i + 4]):
            out.append(int(body[i + 1:i + 4], 8))
            i += 4
        elif e in ('u', 'U') and re.fullmatch('[0-9a-fA-F]{%d}' % (4 if e == 'u' else 8), body[i + 2:i + (6 if e == 'u' else 10)]):
            n = 4 if e == 'u' else 8
            out += chr(int(body[i + 2:i + 2 + n], 16)).encode()
            i += 2 + n
        else:
            raise ValueError('invalid escape in compiled selection literal')
    return bytes(out)


GENERATED_SELECTION = 'internal/nativeartifacts/embedded/selection'
GENERATED_NAMES = ('manifest.json', 'manifest.sha256', 'sandbox-policy.rego')


def compiled_selection(root):
    """The loader's production selection: manifest, reviewed hash, Rego.

    make build generates it next to the embedded archives (git-ignored), and
    the loader prefers it; otherwise the committed default in SELECTION_FILE
    applies. A partial generated selection is refused."""
    generated = root / GENERATED_SELECTION
    present = [os.path.lexists(generated / name) for name in GENERATED_NAMES]
    if any(present):
        require(all(present), 'generated native selection is incomplete; run make build')
        manifest, digest, rego = (read_regular(generated / name) for name in GENERATED_NAMES)
        return manifest, digest.decode(), rego
    return committed_selection(root)


def committed_selection(root):
    """The committed default selection. Each constant must be exactly one Go
    string literal, as the helper-input audit requires; another shape is
    refused rather than guessed."""
    source = read_regular(root / SELECTION_FILE).decode()
    values = []
    for name in SELECTION_NAMES:
        found = re.findall(r'\b' + name + r'\s*=\s*("(?:\\.|[^"\\\n])*"|`[^`]*`)', source)
        require(len(found) == 1, f'compiled selection {name} must be one Go string literal')
        values.append(go_string(found[0]))
    manifest, digest, rego = values
    return manifest, digest.decode(), rego


def check_compiled_selection(selection, vendored, rego_file):
    """The compiled selection is the vendored qualification itself, under its
    separately reviewed hash, with the qualified Rego. All three empty is the
    explicit pending state, never a pass; a partial selection fails."""
    manifest, digest, rego = selection
    if not manifest and not digest and not rego:
        raise SelectionPending('production native selection is unavailable: compiled manifest, reviewed hash and Rego are empty until the published release and helper are qualified')
    require(manifest and digest and rego, 'partial compiled native selection')
    require(hex_string(digest, 64), 'compiled reviewed manifest hash is not a SHA-256')
    require(sha(manifest) == digest, 'compiled manifest differs from its reviewed hash')
    require(manifest == vendored, 'compiled manifest differs from the vendored qualification')
    require(rego == rego_file, 'compiled Rego differs from the vendored qualified Rego')


def read_vendored(root, relative):
    """A file the full compiled selection depends on; absence names the file."""
    path = root / relative
    require(os.path.lexists(path), f'missing vendored qualification file: {relative}')
    return read_regular(path)


def check(root, upstream_source=None, fork_source=None):
    vendor = root / 'third_party/openshell'
    selection = compiled_selection(root)
    if not all(selection):
        check_compiled_selection(selection, b'', b'')  # pending or partial: refused before vendor I/O
    raw = read_vendored(root, 'third_party/openshell/qualification.json')
    check_compiled_selection(selection, raw, read_vendored(root, 'assets/sandbox/openshell-0.1.6-p2.rego'))
    p, q = validate_document(decode(raw), root)
    check_helper_proofs(root, p['helper'])
    proof_raw = read_regular(vendor / 'source-proof.json')
    require(sha(proof_raw) == q['source_proof_sha256'], 'source qualification proof changed')
    proof = fields(decode(proof_raw), ('upstream', 'fork'))
    for key, prefix in (('upstream', 'upstream'), ('fork', 'fork')):
        entry = proof[key]
        require(entry['commit'] == p[prefix + '_commit'] and entry['tree'] == p[prefix + '_tree'], 'source proof does not match selected commit/tree')
        commit = read_regular(vendor / (key + '.commit'))
        require(sha(commit) == entry['commit_object_sha256'], 'commit evidence bytes changed')
        oid = hashlib.sha1(b'commit ' + str(len(commit)).encode() + b'\0' + commit).hexdigest()
        require(oid == entry['commit'] and commit.splitlines()[0] == ('tree ' + entry['tree']).encode(), 'commit does not authenticate selected source tree')
        if key == 'fork':
            require(b'\ngpgsig ' in commit.split(b'\n\n', 1)[0], 'fork commit is not the re-signed release commit')
    for patch in p['patches']:
        require(sha(read_regular(vendor / patch['name'])) == patch['sha256'], 'patch bytes/order changed')
    require(sha(read_regular(vendor / 'bootstrap-fixture.json')) == p['protocol_fixture_sha256'], 'bootstrap/flag fixture changed without qualification')
    rego = read_regular(root / 'assets/sandbox/openshell-0.1.6-p2.rego')
    require(sha(rego) == p['rego_sha256'], 'embedded Rego changed without qualification')
    pins = decode(read_regular(root / 'assets/vm/firecracker/pins.json'))
    for role in ('boundary', 'supervisor'):
        artifact = p['platforms']['amd64'][role]
        mirrored = pins.get('openshell-p2-' + role + '.tgz')
        require(mirrored == {'url': artifact['url'], 'sha256': artifact['archive_sha256']}, 'Firecracker runtime pin mirror differs')
    for arch in ('amd64', 'arm64'):
        build_raw = read_regular(vendor / ('build-proof-' + arch + '.json'))
        require(sha(build_raw) == q['build_proof_sha256'][arch], 'build/reproduction proof changed')
        build = fields(decode(build_raw), ('fork_commit', 'fork_tree', 'architecture', 'rust_version', 'toolchain_images', 'version_stamp', 'builds', 'loaders'))
        require((build['fork_commit'], build['fork_tree'], build['architecture'], build['rust_version']) ==
                (p['fork_commit'], p['fork_tree'], arch, '1.95.0'), 'build proof uses a different source/platform/toolchain')
        require(build['version_stamp'] == {'from': '0.0.0', 'to': p['version'], 'files': ['Cargo.toml', 'Cargo.lock']}, 'unqualified source stamping')
        require(build['toolchain_images'] and all(re.fullmatch(r'[^\s]+@sha256:[0-9a-f]{64}', image) for image in build['toolchain_images']), 'toolchain images are not immutable')
        require(len(build['builds']) == 2 and build['builds'][0] == build['builds'][1], 'two fresh builds did not reproduce identical bytes')
        for role in ('boundary', 'supervisor'):
            artifact = p['platforms'][arch][role]
            require(build['builds'][0][role] == {'archive_sha256': artifact['archive_sha256'], 'binary_sha256': artifact['binary_sha256']}, 'reproduced bytes differ from published asset pins')
            require(build['loaders'][role] == p['platforms'][arch][role + '_loader'], 'loader requirements differ from qualification')
    with tempfile.TemporaryDirectory(prefix='yoloctl-openshell-vendor-') as temp:
        temp = Path(temp)
        base, fork = temp / 'base', temp / 'fork'
        base.mkdir()
        fork.mkdir()
        upstream = source_archive(proof['upstream'], upstream_source, temp / 'upstream.tar.gz')
        released = source_archive(proof['fork'], fork_source, temp / 'fork.tar.gz')
        require(archive_tree(upstream, base) == p['upstream_tree'], 'upstream archive tree differs')
        require(archive_tree(released, fork) == p['fork_tree'], 'released fork archive tree differs')
        for patch in p['patches']:
            require(git(base, 'write-tree') == patch['parent_tree'], 'patch order/parent source changed')
            git(base, 'apply', '--cached', str((vendor / patch['name']).resolve()))
            require(git(base, 'write-tree') == patch['tree'], 'patch result differs from qualified tree')
        require(git(base, 'write-tree') == p['fork_tree'], 'patch stack does not reproduce released fork')
        rego_blob = git(fork, 'show', p['fork_tree'] + ':crates/openshell-supervisor-network/data/sandbox-policy.rego', raw=True)
        require(rego_blob == rego, 'embedded Rego differs from qualified fork source')
    return q['revision']


def check_helper_proofs(root, helper):
    vendor = root / 'third_party/openshell'
    source_raw = read_regular(vendor / 'helper-source-proof.json', 8 << 20)
    require(sha(source_raw) == helper['source_proof_sha256'], 'helper source proof changed')
    proof = decode(source_raw)
    require(proof['inputs'] == helper['inputs'] and proof['go_version'] == helper['go_version'] and
            hex_string(proof['toolchain_sha256'], 64), 'helper source/toolchain proof differs')
    require(proof['recipe_sha256'] == sha(read_regular(root / 'scripts/openshell-helper-recipe.json')) and
            proof['builder_sha256'] == sha(read_regular(root / 'scripts/build-openshell-helper.py')) and
            proof['checker_sha256'] == sha(read_regular(root / 'scripts/check-helper-inputs.py')) and
            proof['inspector_sha256'] == sha(read_regular(root / 'scripts/openshell-helper-inputs.go')), 'helper build recipe changed')
    require(proof['mutations'] == 'PASS: added/edited production source bound; outside CLI edit unchanged; no make/generate',
            'helper no-make source mutation qualification is missing')
    # Explicit vendor maintenance may compile the input inspector, never the
    # helper or Rust runtime. Ordinary builds/downloads do not invoke this.
    env = dict(os.environ)
    env.update(GOFLAGS='', GOENV='off', GOWORK='off', GOTOOLCHAIN='local', CGO_ENABLED='0')
    current = subprocess.run(['python3', 'scripts/check-helper-inputs.py'], cwd=root, env=env,
                             capture_output=True, timeout=600)
    require(current.returncode == 0, 'compiled helper source inventory check failed: ' + current.stderr.decode(errors='replace')[-2000:])
    current = decode(current.stdout)
    require(current['inputs'] == helper['inputs'] and current['actual_local_build_inputs'] == proof['actual_local_build_inputs'],
            'compiled helper source/dependency inputs differ from qualification')
    for arch in ('amd64', 'arm64'):
        raw = read_regular(vendor / ('helper-build-proof-' + arch + '.json'), 8 << 20)
        require(sha(raw) == helper['build_proof_sha256'][arch], 'helper paired-build proof changed')
        build = decode(raw)
        require(build['architecture'] == arch and build['go_version'] == helper['go_version'] and
                build['toolchain_sha256'] == proof['toolchain_sha256'] and build['inputs'] == helper['inputs'] and
                build['source_proof_sha256'] == helper['source_proof_sha256'], 'helper producer/source binding differs')
        expected = {key: helper['platforms'][arch][key] for key in ('binary_sha256', 'archive_sha256')}
        require(build['builds'] == [expected, expected], 'helper two clean builds did not reproduce pinned bytes')
        reports = build['producer_reports']
        require(len(reports) == 2 and reports[0] == reports[1] and reports[0]['loader'] == 'static' and
                reports[0]['inputs'] == helper['inputs'] and reports[0]['go_version'] == helper['go_version'] and
                reports[0]['architecture'] == arch and reports[0]['binary_sha256'] == expected['binary_sha256'],
                'helper static ELF/producer proof differs')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', required=True)
    parser.add_argument('--upstream-source', type=Path, help='offline exact upstream archive')
    parser.add_argument('--fork-source', type=Path, help='offline exact released fork archive')
    args = parser.parse_args()
    try:
        revision = check(ROOT, args.upstream_source, args.fork_source)
    except SelectionPending as pending:
        parser.exit(2, f'OpenShell qualification PENDING: {pending}\n')
    except (ValueError, OSError, KeyError, TypeError, json.JSONDecodeError, tarfile.TarError, subprocess.TimeoutExpired) as error:
        parser.exit(1, f'OpenShell qualification FAILED: {error}\n')
    print(f'OpenShell source, patch stack, runtime pins and qualification {revision} match')


if __name__ == '__main__':
    main()
