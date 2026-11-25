import importlib.util
import json
import os
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('openshell_build', HERE / 'openshell-build.py')
ob = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ob)

# Reviewed archives retained by the release lanes (optional; absent outside the team checkout).
EVIDENCE = Path(os.environ.get('OPENSHELL_REVIEWED_ARCHIVES',
                               HERE.parent / '.yolo-team' / 'tasks' / 'build-openshell-upgrade' / 'devops-arm64-cross-r1' / 'finished-release'))


class OpenShellBuildTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.pins_dir = self.tmp / 'openshell'
        shutil.copytree(ob.PINS_DIR, self.pins_dir)
        self.pins = ob.load_pins(self.pins_dir)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def entry(self, name='entry'):
        out = self.tmp / name
        out.mkdir()
        binaries = {}
        for arch in ob.ARCHES:
            binaries[arch] = {}
            for binary in ob.BINARIES:
                (out / ('%s-%s.tar.gz' % (binary, arch))).write_bytes(('%s %s' % (binary, arch)).encode())
                binaries[arch][binary] = '%064d' % len(binaries[arch])
        shutil.copyfile(self.pins_dir / self.pins['rego']['file'], out / 'sandbox-policy.rego')
        return out, binaries

    def test_committed_pins_are_consistent(self):
        ob.check(ob.load_pins())

    def test_patch_change_is_refused_and_changes_the_key(self):
        key = ob.cache_key(self.pins, self.pins_dir)
        self.assertEqual(key, ob.cache_key(self.pins, self.pins_dir))
        patch = self.pins_dir / self.pins['patches'][0]['file']
        patch.write_bytes(patch.read_bytes() + b'\n')
        self.assertNotEqual(key, ob.cache_key(self.pins, self.pins_dir))
        with self.assertRaisesRegex(ob.BuildError, 'hash differs from pins'):
            ob.check(self.pins, self.pins_dir)

    def test_pin_change_changes_the_key(self):
        key = ob.cache_key(self.pins, self.pins_dir)
        pins = self.pins_dir / 'pins.json'
        pins.write_text(pins.read_text().replace('"1.95.0"', '"1.96.0"'))
        self.assertNotEqual(key, ob.cache_key(self.pins, self.pins_dir))

    def test_cache_hit_requires_complete_entry_matching_its_receipt(self):
        cache = self.tmp / 'cache'
        cache.mkdir()
        out, binaries = self.entry('cache/k')
        self.assertIsNone(ob.cached(cache, 'k'))
        ob.write_qualify_inputs(self.pins, self.pins_dir, 'k', out, binaries, {})
        archives = {f.name: ob.sha256_file(f) for f in out.iterdir() if f.name.endswith(('.tar.gz', '.rego'))}
        (out / 'RECEIPT.json').write_text(json.dumps({'archives': archives}))
        self.assertEqual(ob.cached(cache, 'k'), out)
        (out / 'openshell-sandbox-arm64.tar.gz').write_bytes(b'changed')
        self.assertIsNone(ob.cached(cache, 'k'))

    def test_qualify_inputs_point_at_the_entrys_own_evidence(self):
        out, binaries = self.entry()
        ob.write_qualify_inputs(self.pins, self.pins_dir, 'k', out, binaries, {'rustc': 'rustc 1.95.0'})
        inputs = json.loads((out / 'release-inputs.json').read_text())
        self.assertEqual(inputs['revision'], 'local-k')
        self.assertEqual(inputs['build_proofs'], {'amd64': 'build-amd64.json', 'arm64': 'build-arm64.json'})
        for name in [inputs['patch2_record'], inputs['source_proof']] + list(inputs['build_proofs'].values()):
            self.assertTrue((out / name).is_file(), name)
        self.assertIn(inputs['patch2_sha256'], (out / inputs['patch2_record']).read_text())
        self.assertEqual(inputs['rego'], json.loads((self.pins_dir / 'release-inputs.json').read_text())['rego'])
        boundary = inputs['platforms']['arm64']['boundary']
        self.assertEqual(boundary['archive'], 'openshell-sandbox-arm64.tar.gz')
        self.assertEqual(boundary['archive_sha256'], ob.sha256_file(out / 'openshell-sandbox-arm64.tar.gz'))
        self.assertEqual(boundary['binary_sha256'], binaries['arm64']['openshell-sandbox'])

    def test_fork_commit_names_the_built_tree_on_the_pinned_upstream(self):
        out, binaries = self.entry()
        ob.write_qualify_inputs(self.pins, self.pins_dir, 'k', out, binaries, {})
        inputs = json.loads((out / 'release-inputs.json').read_text())
        body = ob.source_commit_body(self.pins)
        hashed = subprocess.run(['git', 'hash-object', '-t', 'commit', '--stdin'], input=body,
                                capture_output=True, check=True).stdout.decode().strip()
        self.assertEqual(inputs['fork_commit'], hashed)
        lines = body.decode().splitlines()
        self.assertEqual(lines[0], 'tree ' + inputs['fork_tree'])
        self.assertEqual(lines[1], 'parent ' + self.pins['upstream']['commit'])
        proof = json.loads((out / inputs['source_proof']).read_text())
        self.assertEqual(proof['source_commit'], inputs['fork_commit'])

    def test_qualify_refuses_inputs_of_another_source(self):
        # The first-build inputs (OpenShell 0.1.6, two patches), frozen here,
        # must not be qualified as the current source.
        historical = {
            'revision': 'first-build-1', 'fork_commit': 'e8605009bed50a05136efe106a0c1e2f283ee771',
            'fork_tree': '9112ab178200f6751290ac2c2a20150e31c8708b',
            'patch2_sha256': '77019fe849082581402dc21b366e22afe633973a40aa16d5eba1d66ef8a23b0e',
            'patch2_record': 'record', 'source_proof': 'proof', 'build_proofs': {'amd64': 'a', 'arm64': 'b'},
            'rego': 'third_party/openshell/sandbox-policy-0.1.6.rego', 'platforms': {}}
        path = self.tmp / 'historical-inputs.json'
        path.write_text(json.dumps(historical))
        run = subprocess.run(['go', 'run', '-buildvcs=false', './scripts/native-artifacts.go', 'qualify',
                              str(path), str(self.tmp), str(self.tmp)], cwd=HERE.parent,
                             env=dict(os.environ, GOFLAGS=''), capture_output=True, text=True)
        self.assertNotEqual(run.returncode, 0)
        self.assertIn('inputs describe another OpenShell source than third_party/openshell/pins.json', run.stderr)

    def test_missing_host_tool_names_the_setup(self):
        which = ob.shutil.which
        ob.shutil.which = lambda name: None if name == 'zig' else '/usr/bin/' + name
        try:
            with self.assertRaisesRegex(ob.BuildError, 'zig not found: install rustup'):
                ob.toolchain(self.pins)
        finally:
            ob.shutil.which = which

    @unittest.skipUnless(EVIDENCE.is_dir(), 'reviewed release archives not present')
    def test_pack_reproduces_the_reviewed_archives(self):
        for arch in ob.ARCHES:
            for binary in ob.BINARIES:
                name = '%s-%s.tar.gz' % (binary, arch)
                work = self.tmp / arch / binary
                work.mkdir(parents=True)
                with tarfile.open(EVIDENCE / name) as t:
                    self.assertEqual(t.getnames(), [binary])
                    t.extractall(work, filter='data')
                self.assertEqual(ob.sha256_file(work / binary), self.pins['outputs'][arch][binary]['binary_sha256'])
                ob.pack(work / binary, self.tmp / name)
                self.assertEqual(ob.sha256_file(self.tmp / name), self.pins['outputs'][arch][binary]['archive_sha256'], name)


if __name__ == '__main__':
    unittest.main()
