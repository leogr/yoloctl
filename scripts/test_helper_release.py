"""Mocked helper runner evidence/ownership regressions; no Go or VM execution."""
import contextlib
import hashlib
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('helper_release', Path(__file__).with_name('build-openshell-helper.py'))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class RunnerStatusTests(unittest.TestCase):
    def exercise(self, checkpoint=None, error_type=KeyboardInterrupt, existing=False):
        with tempfile.TemporaryDirectory(prefix='helper-runner-status-') as temporary:
            root = Path(temporary)
            native, toolchain, source = root / 'native', root / 'go', root / 'source'
            for directory in (native, toolchain, source / 'scripts'):
                directory.mkdir(parents=True)
            for name in ('openshell-helper-recipe.json', 'check-helper-inputs.py', 'openshell-helper-inputs.go'):
                (source / 'scripts' / name).write_text('public fixture\n')
            output = root / 'result'
            if existing:
                output.mkdir()
                (output / 'runner.exit').write_text('preserve\n')
            identity = {'schema': 1, 'source_sha256': 'c' * 64, 'modules': []}
            counters = {'build': 0, 'inputs': 0, 'clean': 0}
            checker = SimpleNamespace(check=lambda *args, **kwargs: {'inputs': identity, 'mutations': 'mocked status test only'})

            def check(*args, **kwargs):
                if checkpoint == 'before-build':
                    raise error_type('before build fixture')
                return {'inputs': identity, 'mutations': 'mocked status test only'}

            checker.check = check

            def go_run(root, arguments, log=None, extra=None):
                self.assertFalse(existing, 'refused output must not run Go or cleanup')
                self.assertFalse((output / 'runner.exit').exists(), 'status published before all checks/cleanup')
                if arguments == ['env', 'GOVERSION']:
                    return 'go1.27.3\n'
                if arguments == ['env', 'GOROOT']:
                    return str(toolchain) + '\n'
                if arguments == ['mod', 'verify']:
                    return ''
                if arguments[0] == 'clean':
                    counters['clean'] += 1
                    if checkpoint == 'cleanup':
                        raise error_type('cleanup fixture')
                    return ''
                if arguments[0] == 'build':
                    counters['build'] += 1
                    if checkpoint == 'during-build' and counters['build'] == 2:
                        raise error_type('during build fixture')
                    binary = Path(arguments[arguments.index('-o') + 1])
                    binary.write_bytes(('not-executable fixture ' + extra['GOARCH']).encode())
                    return ''
                if '--binary' in arguments:
                    binary = Path(arguments[arguments.index('--binary') + 1])
                    arch = arguments[arguments.index('--arch') + 1]
                    return json.dumps({'inputs': identity, 'go_version': 'go1.27.3', 'architecture': arch,
                                       'loader': 'static', 'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest()})
                counters['inputs'] += 1
                if checkpoint == 'final-inputs' and counters['inputs'] == 2:
                    raise error_type('final input fixture')
                return json.dumps(identity)

            fake_spec = SimpleNamespace(loader=SimpleNamespace(exec_module=lambda module: None))
            argv = ['builder', '--out', str(output), '--native-root', str(native), '--go-version', 'go1.27.3',
                    '--toolchain-sha256', 'a' * 64, '--source-sha256', identity['source_sha256']]
            status = 0
            with mock.patch.object(runner, 'ROOT', source), mock.patch.object(runner, 'go_run', side_effect=go_run), \
                 mock.patch.object(runner, 'toolchain_digest', return_value='a' * 64), \
                 mock.patch.object(runner.shutil, 'disk_usage', return_value=SimpleNamespace(free=16 << 30)), \
                 mock.patch.object(runner.importlib.util, 'spec_from_file_location', return_value=fake_spec), \
                 mock.patch.object(runner.importlib.util, 'module_from_spec', return_value=checker), \
                 mock.patch('sys.argv', argv), contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                try:
                    runner.main()
                except SystemExit as error:
                    status = error.code
            saved = (output / 'runner.exit').read_text()
            proofs = sorted(p.name for p in output.glob('build-proof-*.json'))
            return status, saved, counters, proofs

    def test_interruptions_and_unexpected_errors_never_publish_success(self):
        for checkpoint in ('before-build', 'during-build', 'final-inputs'):
            for error_type in (KeyboardInterrupt, KeyError, SystemExit):
                with self.subTest(checkpoint=checkpoint, error=error_type):
                    status, saved, counters, _ = self.exercise(checkpoint, error_type)
                    self.assertNotEqual(status, 0)
                    self.assertNotEqual(saved, '0\n')
                    self.assertEqual(counters['clean'], 1)

    def test_cleanup_failure_overrides_completed_builds(self):
        status, saved, _, proofs = self.exercise('cleanup', ValueError)
        self.assertNotEqual(status, 0)
        self.assertNotEqual(saved, '0\n')
        self.assertEqual(len(proofs), 2)

    def test_only_completed_pairs_and_final_checks_publish_success(self):
        status, saved, counters, proofs = self.exercise()
        self.assertEqual((status, saved), (0, '0\n'))
        self.assertEqual(counters, {'build': 4, 'inputs': 2, 'clean': 1})
        self.assertEqual(proofs, ['build-proof-amd64.json', 'build-proof-arm64.json'])

    def test_existing_output_is_refused_without_modification(self):
        status, saved, counters, _ = self.exercise(existing=True)
        self.assertNotEqual(status, 0)
        self.assertEqual(saved, 'preserve\n')
        self.assertEqual(counters, {'build': 0, 'inputs': 0, 'clean': 0})


if __name__ == '__main__':
    unittest.main()
