"""Candidate provenance, confinement config and live-gate selection regressions."""
import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("linux7", Path(__file__).with_name("linux7.py"))
linux7 = importlib.util.module_from_spec(spec)
spec.loader.exec_module(linux7)


class Linux7Tests(unittest.TestCase):
    def setUp(self):
        self.inputs = linux7.load_builder().builder.inputs
        self.config = '\n'.join(
            [f'CONFIG_{name}=y' for name in self.inputs.KERNEL_EXTRAS]
            + [f'# CONFIG_{name} is not set' for name in self.inputs.KERNEL_DISABLED]
            + ['CONFIG_LSM="landlock,lockdown,yama"']) + '\n'

    def fixture(self, root):
        (root / 'kernel').write_bytes(b'candidate-kernel')
        (root / 'kernel.config').write_text(self.config)
        manifest = {
            'inputs': {name: dict(self.inputs.PINS[name]) for name in sorted(self.inputs.KERNEL_INPUTS)},
            'enabled': self.inputs.KERNEL_EXTRAS, 'disabled': self.inputs.KERNEL_DISABLED,
            'kernel_sha256': self.inputs.digest(root / 'kernel'),
            'config_sha256': self.inputs.digest(root / 'kernel.config'),
        }
        (root / 'kernel.json').write_text(json.dumps(manifest))
        (root / 'provenance.json').write_text(json.dumps({'kernel': manifest}))
        (root / 'image.json').write_text(json.dumps({'kernel_sha256': manifest['kernel_sha256']}))
        return manifest

    def test_qualification_matches_bundled_production_recipe(self):
        spec = importlib.util.spec_from_file_location('production_inputs',
                        linux7.REPO / 'assets/vm/firecracker/inputs.py')
        production = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(production)
        self.assertEqual(production.PINS[linux7.SOURCE], linux7.SOURCE_PIN)
        self.assertEqual(production.KERNEL_INPUTS, {linux7.SOURCE, linux7.CONFIG})
        self.assertEqual(production.KERNEL_DISABLED, self.inputs.KERNEL_DISABLED)
        self.assertIn('PSI', self.inputs.KERNEL_EXTRAS)
        self.assertIn('PSI_DEFAULT_DISABLED', self.inputs.KERNEL_DISABLED)

    def test_missing_runtime_requirements_fail_before_compilation(self):
        linux7.validate_config(self.config, self.inputs)
        for option in ['SECCOMP_FILTER', 'MEMCG', 'CGROUP_PIDS', 'FUSE_FS',
                       'VIRTIO_BLK', 'VIRTIO_VSOCKETS', 'SECURITY_LANDLOCK', 'NFT_REJECT_INET']:
            with self.subTest(option=option), self.assertRaisesRegex(RuntimeError, option):
                linux7.validate_config(self.config.replace(f'CONFIG_{option}=y', f'CONFIG_{option}=m'), self.inputs)
        with self.assertRaisesRegex(RuntimeError, 'activate Landlock'):
            linux7.validate_config(self.config.replace('"landlock,', '"notlandlock,'), self.inputs)
        with self.assertRaisesRegex(RuntimeError, 'PSI_DEFAULT_DISABLED'):
            linux7.validate_config(self.config.replace('# CONFIG_PSI_DEFAULT_DISABLED is not set',
                                                      'CONFIG_PSI_DEFAULT_DISABLED=y'), self.inputs)

    def test_nested_kvm_must_be_built_in(self):
        lines = self.config.splitlines(keepends=True)
        for option in ['VIRTUALIZATION', 'KVM', 'KVM_INTEL', 'KVM_AMD']:
            enabled = f'CONFIG_{option}=y\n'
            for change, config in [
                    ('absent', ''.join(line for line in lines if line != enabled)),
                    ('module', ''.join(f'CONFIG_{option}=m\n' if line == enabled else line for line in lines))]:
                with self.subTest(option=option, change=change), \
                        self.assertRaisesRegex(RuntimeError, rf'lacks {option}$'):
                    linux7.validate_config(config, self.inputs)

    def test_artifact_and_recipe_mismatch_are_rejected(self):
        for change in ['kernel', 'config', 'source', 'options', 'image']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp).resolve()
                manifest = self.fixture(root)
                linux7.verify_kernel(root, self.inputs)
                linux7.verify_image(root, self.inputs)
                if change == 'kernel':
                    (root / 'kernel').write_bytes(b'other kernel')
                elif change == 'config':
                    (root / 'kernel.config').write_text(self.config + '# changed\n')
                elif change == 'source':
                    manifest['inputs'][linux7.SOURCE]['sha256'] = '0' * 64
                elif change == 'options':
                    manifest['enabled'] = []
                else:
                    (root / 'image.json').write_text(json.dumps({'kernel_sha256': '0' * 64}))
                (root / 'kernel.json').write_text(json.dumps(manifest))
                (root / 'provenance.json').write_text(json.dumps({'kernel': manifest}))
                if change != 'image':
                    with self.assertRaisesRegex(RuntimeError, 'pinned Linux 7'):
                        linux7.verify_kernel(root, self.inputs)
                with self.assertRaisesRegex(RuntimeError, 'pinned Linux 7'):
                    linux7.verify_image(root, self.inputs)

    def test_live_gate_pins_initial_update_and_import_to_candidate(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            self.fixture(root)
            cli = root / 'yoloctl'
            cli.write_bytes(b'fixture')
            args = SimpleNamespace(image=str(root), output=str(root / 'trial'), yoloctl=str(cli))
            with mock.patch.object(self.inputs, 'require_platform'), mock.patch.object(self.inputs, 'run') as run:
                with mock.patch.dict('os.environ', {'SMOKE_VM_IMAGE': '/old', 'SMOKE_UPDATE_VM_IMAGE': '/old',
                                                   'SMOKE_IMPORT_BACKEND': 'lima', 'SMOKE_WORKDIR': '/other/project',
                                                   'SMOKE_CLEANUP_FAILED': '1', 'SMOKE_PRESERVE_SPACE': '1'}):
                    linux7.smoke(args, self.inputs)
            env = run.call_args.kwargs['env']
            self.assertEqual(env['SMOKE_VM_IMAGE'], str(root))
            self.assertEqual(env['SMOKE_UPDATE_VM_IMAGE'], str(root))
            self.assertEqual(env['SMOKE_IMPORT_BACKEND'], 'firecracker')
            self.assertEqual(env['SMOKE_EXPECT_KERNEL_RELEASE'], linux7.VERSION)
            self.assertEqual(env['SMOKE_WORKDIR'], str(root / 'trial/workdir'))
            self.assertNotIn('SMOKE_CLEANUP_FAILED', env)
            self.assertNotIn('SMOKE_PRESERVE_SPACE', env)
            # A retry cannot overwrite an existing fixture, including a running VM.
            with mock.patch.object(self.inputs, 'require_platform'), self.assertRaises(FileExistsError):
                linux7.smoke(args, self.inputs)


if __name__ == '__main__':
    unittest.main()
