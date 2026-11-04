"""Qualification refusals and safe source-tree reconstruction; no network/builds."""
import copy
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('vendor_openshell', Path(__file__).with_name('vendor-openshell.py'))
vendor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(vendor)


def fixture():
    p = {
        'version': vendor.go_constant(vendor.ROOT, 'qualifiedVersion'),
        'upstream_commit': vendor.go_constant(vendor.ROOT, 'qualifiedUpstreamCommit'),
        'upstream_tree': vendor.go_constant(vendor.ROOT, 'qualifiedUpstreamTree'),
        'fork_commit': 'a' * 40, 'fork_tree': 'b' * 40,
        'patches': [
            {'name': 'unmediated-network.patch', 'sha256': vendor.go_constant(vendor.ROOT, 'qualifiedPatch1SHA256'),
             'parent_tree': vendor.go_constant(vendor.ROOT, 'qualifiedUpstreamTree'), 'tree': vendor.go_constant(vendor.ROOT, 'qualifiedPatch1Tree')},
            {'name': 'workspace-read-only.patch', 'sha256': vendor.sha(b'fixture-patch2'),
             'parent_tree': vendor.go_constant(vendor.ROOT, 'qualifiedPatch1Tree'), 'tree': 'b' * 40},
        ],
        'platforms': {}, 'rego_sha256': vendor.sha(b'fixture-rego'), 'bootstrap_schema': 2,
        'protocol_fixture_version': vendor.go_constant(vendor.ROOT, 'protocolFixtureVersion'), 'protocol_fixture_sha256': vendor.sha(b'fixture-bootstrap'),
        'helper': {'protocol': vendor.go_constant(vendor.ROOT, 'HelperProtocol', 'internal/openshell/helper.go'),
                   'inputs': {'schema': 1, 'source_sha256': vendor.sha(b'helper-source-fixture'),
                              'modules': [{'path': path, 'version': version, 'sum': 'h1:test-only'} for path, version in
                                          [('golang.org/x/crypto', 'v0.54.0'), ('golang.org/x/mod', 'v0.35.0'), ('golang.org/x/sys', 'v0.47.0'), ('gopkg.in/yaml.v3', 'v3.0.1')]]},
                   'go_version': 'go1.27.0', 'source_proof_sha256': vendor.sha(b'helper-source-proof-fixture'),
                   'build_proof_sha256': {arch: vendor.sha(('helper-' + arch + '-proof-fixture').encode()) for arch in ('amd64', 'arm64')},
                   'platforms': {arch: {'url': f'embedded:helper-{arch}.tar.gz',
                                        'archive_sha256': vendor.sha(('helper-' + arch + '-archive-fixture').encode()),
                                        'binary_sha256': vendor.sha(('helper-' + arch + '-binary-fixture').encode()),
                                        'member': 'yoloctl-openshell-helper'} for arch in ('amd64', 'arm64')}},
    }
    for arch in ('amd64', 'arm64'):
        artifacts = {}
        for role, member in (('boundary', 'openshell-sandbox'), ('supervisor', 'openshell-supervisor')):
            artifacts[role] = {'url': f'embedded:{member}-{arch}.tar.gz',
                               'archive_sha256': vendor.sha(('archive-fixture-' + arch + role).encode()),
                               'binary_sha256': vendor.sha(('binary-fixture-' + arch + role).encode()), 'member': member}
        artifacts.update(boundary_loader='test-only-static', supervisor_loader='test-only-static')
        p['platforms'][arch] = artifacts
    return {'pins': p, 'qualification': {'revision': 'test-fixture', 'subject_sha256': vendor.sha(vendor.subject_bytes(p)),
                                       'source_proof_sha256': vendor.sha(b'fixture-source-proof'),
                                       'build_proof_sha256': {arch: vendor.sha(arch.encode()) for arch in ('amd64', 'arm64')}}}


class QualificationTests(unittest.TestCase):
    def test_complete_fixture_matches_shared_go_encoding(self):
        document = fixture()
        vendor.validate_document(document)
        # Stored fixture is explicit test data, never a release qualification.
        shared = vendor.decode((vendor.ROOT / 'internal/openshell/testdata/qualification-fixture.json').read_bytes())
        self.assertEqual(shared, document)
        self.assertEqual(vendor.sha(vendor.subject_bytes(shared['pins'])), shared['qualification']['subject_sha256'])

    def test_each_pin_change_rejects_old_qualification(self):
        changes = [
            lambda p: p.update(version='0.1.7'), lambda p: p.update(upstream_commit='c' * 40),
            lambda p: p.update(fork_tree='c' * 40), lambda p: p.update(bootstrap_schema=1),
            lambda p: p.update(protocol_fixture_sha256=vendor.sha(b'changed flag')),
            lambda p: p.update(rego_sha256=vendor.sha(b'changed rego')),
            lambda p: p['patches'].reverse(), lambda p: p['patches'].pop(),
            lambda p: p['patches'][1].update(sha256=vendor.sha(b'changed patch')),
            lambda p: p['platforms']['amd64']['supervisor'].update(binary_sha256=vendor.sha(b'stale supervisor')),
            lambda p: p['platforms'].pop('arm64'),
            lambda p: p['helper'].update(protocol='1'),
            lambda p: p['helper']['inputs'].update(source_sha256=vendor.sha(b'changed helper source')),
            lambda p: p['helper']['platforms'].pop('arm64'),
        ]
        for change in changes:
            with self.subTest(change=change):
                d = fixture()
                change(d['pins'])
                with self.assertRaises(ValueError):
                    vendor.validate_document(d)

    def test_old_schema_and_source_still_refused_when_subject_is_regenerated(self):
        for key, value in [('version', '0.1.7'), ('bootstrap_schema', 1), ('protocol_fixture_version', 'old'), ('upstream_tree', 'c' * 40)]:
            d = fixture()
            d['pins'][key] = value
            d['qualification']['subject_sha256'] = vendor.sha(vendor.subject_bytes(d['pins']))
            with self.assertRaises(ValueError):
                vendor.validate_document(d)

    def test_unknown_and_duplicate_fields_refused(self):
        with self.assertRaises(ValueError):
            vendor.decode(b'{"pins":{},"pins":{}}')
        d = fixture()
        d['pins']['unreviewed'] = True
        with self.assertRaises(ValueError):
            vendor.validate_document(d)

    def test_fixture_objects_are_not_modified_by_check(self):
        d = fixture()
        original = copy.deepcopy(d)
        vendor.validate_document(d)
        self.assertEqual(d, original)

    def test_source_archive_hash_mismatch_fails_offline(self):
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            source = temp / 'source.tar.gz'
            source.write_bytes(b'changed')
            entry = {'commit': 'a' * 40, 'tree': 'b' * 40, 'archive_url': 'https://github.com/NVIDIA/OpenShell/archive/' + 'a' * 40 + '.tar.gz',
                     'archive_sha256': vendor.sha(b'original'), 'commit_object_sha256': vendor.sha(b'commit')}
            with self.assertRaisesRegex(ValueError, 'SHA256 mismatch'):
                vendor.source_archive(entry, source, temp / 'out.tar.gz')

    def test_source_archive_accepts_only_upstream_even_with_local_bytes(self):
        with tempfile.TemporaryDirectory() as temp:
            temp = Path(temp)
            source = temp / 'source.tar.gz'
            source.write_bytes(b'bounded source archive fixture')
            for repository in ('NVIDIA/OpenShell', 'leogr/OpenShell'):
                with self.subTest(repository=repository):
                    destination = temp / repository.split('/')[0]
                    entry = {'commit': 'a' * 40, 'tree': 'b' * 40,
                             'archive_url': f'https://github.com/{repository}/archive/' + 'a' * 40 + '.tar.gz',
                             'archive_sha256': vendor.sha(source.read_bytes()),
                             'commit_object_sha256': vendor.sha(b'commit')}
                    if repository == 'NVIDIA/OpenShell':
                        self.assertEqual(vendor.source_archive(entry, source, destination), destination)
                        self.assertEqual(destination.read_bytes(), source.read_bytes())
                    else:
                        with self.assertRaisesRegex(ValueError, 'source URL'):
                            vendor.source_archive(entry, source, destination)
                        self.assertFalse(destination.exists())


class CompiledSelectionTests(unittest.TestCase):
    """The loader's compiled production selection, as --check reads it."""

    def selection_root(self, manifest='""', digest='""', rego='""'):
        root = Path(tempfile.mkdtemp())
        (root / 'assets').mkdir()
        (root / vendor.SELECTION_FILE).write_text(
            'package assets\n\nconst (\n'
            f'\tnativeOpenShellManifest       = {manifest}\n'
            f'\tnativeOpenShellManifestSHA256 = {digest}\n'
            f'\tnativeOpenShellRego           = {rego}\n)\n')
        return root

    def test_shipped_selection_is_explicitly_pending(self):
        # The committed default never carries a machine's local selection.
        self.assertEqual(vendor.committed_selection(vendor.ROOT), (b'', '', b''))
        if any(vendor.compiled_selection(vendor.ROOT)):
            self.skipTest('make build generated a selection')
        with self.assertRaises(vendor.SelectionPending):
            vendor.check(vendor.ROOT)

    def test_generated_selection_is_preferred_and_must_be_complete(self):
        root = self.selection_root()
        generated = root / vendor.GENERATED_SELECTION
        generated.mkdir(parents=True)
        (generated / 'manifest.json').write_bytes(b'{"a":"b"}')
        with self.assertRaisesRegex(ValueError, 'incomplete; run make build'):
            vendor.compiled_selection(root)
        (generated / 'manifest.sha256').write_bytes(b'c' * 64)
        (generated / 'sandbox-policy.rego').write_bytes(b'package sandbox\n')
        self.assertEqual(vendor.compiled_selection(root), (b'{"a":"b"}', 'c' * 64, b'package sandbox\n'))

    def test_go_literals_decode_like_the_go_compiler(self):
        rego = 'package sandbox\nallow { input.p == `x` }\n'
        root = self.selection_root(manifest='`{"a":"b"}`', digest='"' + 'c' * 64 + '"',
                                   rego=json.dumps(rego).replace('`', '\\x60'))
        manifest, digest, got = vendor.compiled_selection(root)
        self.assertEqual((manifest, digest, got), (b'{"a":"b"}', 'c' * 64, rego.encode()))
        self.assertEqual(vendor.go_string('"\\t\\101\\u00e9\\U0001F600"'), '\tAé😀'.encode())
        self.assertEqual(vendor.go_string('`a\r\nb`'), b'a\nb')

    def test_selection_shape_must_be_one_literal(self):
        for source in ('package assets\n', 'package assets\nconst nativeOpenShellManifest = "a" + "b"\n'):
            root = self.selection_root()
            (root / vendor.SELECTION_FILE).write_text(source)
            with self.assertRaisesRegex(ValueError, 'must be one Go string literal'):
                vendor.compiled_selection(root)
        with self.assertRaisesRegex(ValueError, 'invalid escape'):
            vendor.go_string('"\\q"')

    def test_selection_matches_vendored_qualification_and_rego(self):
        manifest, rego = b'{"pins":{}}', b'package sandbox\n'
        good = (manifest, vendor.sha(manifest), rego)
        vendor.check_compiled_selection(good, manifest, rego)
        for selection, vendored, rego_file, message in (
            ((manifest, '', b''), manifest, rego, 'partial compiled native selection'),
            ((b'', vendor.sha(manifest), rego), manifest, rego, 'partial compiled native selection'),
            ((manifest, 'not-a-hash', rego), manifest, rego, 'not a SHA-256'),
            ((manifest, vendor.sha(b'stale'), rego), manifest, rego, 'differs from its reviewed hash'),
            (good, b'{"pins":{"x":1}}', rego, 'differs from the vendored qualification'),
            (good, manifest, b'package other\n', 'differs from the vendored qualified Rego'),
        ):
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                vendor.check_compiled_selection(selection, vendored, rego_file)
        with self.assertRaises(vendor.SelectionPending):
            vendor.check_compiled_selection((b'', '', b''), manifest, rego)

    def test_full_selection_names_a_missing_vendored_file(self):
        manifest = b'{"pins":{}}'
        root = self.selection_root(manifest='`' + manifest.decode() + '`', digest='"' + vendor.sha(manifest) + '"', rego='"package sandbox\\n"')
        with self.assertRaisesRegex(ValueError, 'missing vendored qualification file: third_party/openshell/qualification.json'):
            vendor.check(root)
        (root / 'third_party/openshell').mkdir(parents=True)
        (root / 'third_party/openshell/qualification.json').write_bytes(manifest)
        with self.assertRaisesRegex(ValueError, 'missing vendored qualification file: assets/sandbox/openshell-0.1.6-p2.rego'):
            vendor.check(root)

    def test_partial_compiled_selection_fails_before_reading_vendor_files(self):
        root = self.selection_root(digest='"' + 'd' * 64 + '"')
        with self.assertRaisesRegex(ValueError, 'partial compiled native selection'):
            vendor.check(root)


class ArchiveTreeTests(unittest.TestCase):
    def archive(self, root, items):
        archive = root / 'source.tar.gz'
        with tarfile.open(archive, 'w:gz') as out:
            directory = tarfile.TarInfo('openshell')
            directory.type = tarfile.DIRTYPE
            out.addfile(directory)
            for name, mode, body, kind in items:
                entry = tarfile.TarInfo('openshell/' + name)
                entry.type, entry.mode = kind, mode
                if kind == tarfile.SYMTYPE:
                    entry.linkname = body.decode()
                    out.addfile(entry)
                elif kind == tarfile.REGTYPE:
                    entry.size = len(body)
                    out.addfile(entry, io.BytesIO(body))
                else:
                    out.addfile(entry)
        return archive

    def test_blob_tree_covers_bytes_modes_symlinks_without_extraction(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            repo = root / 'git'
            repo.mkdir()
            archive = self.archive(root, [('a', 0o644, b'contents\n', tarfile.REGTYPE), ('executable', 0o755, b'exec\n', tarfile.REGTYPE), ('link', 0o777, b'a', tarfile.SYMTYPE)])
            tree = vendor.archive_tree(archive, repo)
            self.assertEqual(len(tree), 40)
            self.assertEqual(vendor.git(repo, 'show', tree + ':a', raw=True), b'contents\n')
            self.assertIn('100755 blob', vendor.git(repo, 'ls-tree', tree, 'executable'))
            self.assertIn('120000 blob', vendor.git(repo, 'ls-tree', tree, 'link'))
            self.assertFalse((repo / 'a').exists())
            self.assertFalse((repo / 'link').exists())

    def test_traversal_duplicates_specials_and_symlink_ancestors_refused(self):
        for items in [
            [('../outside', 0o644, b'x', tarfile.REGTYPE)],
            [('a', 0o644, b'x', tarfile.REGTYPE), ('a', 0o644, b'y', tarfile.REGTYPE)],
            [('a', 0o644, b'', tarfile.FIFOTYPE)],
            [('a', 0o777, b'/outside', tarfile.SYMTYPE), ('a/b', 0o644, b'x', tarfile.REGTYPE)],
        ]:
            with tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                repo = root / 'git'
                repo.mkdir()
                with self.assertRaises(ValueError):
                    vendor.archive_tree(self.archive(root, items), repo)
                self.assertFalse((root / 'outside').exists())


if __name__ == '__main__':
    unittest.main()
