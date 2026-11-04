"""Image path and provenance checks without running a VM or installing packages."""
import hashlib
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('microvm_builder', Path(__file__).with_name('build.py'))
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class BuilderTests(unittest.TestCase):
    def test_retired_openshell_is_not_provisioned_or_redirected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "opt/openshell/bin").mkdir(parents=True)
            (root / "opt/openshell/bin/openshell-sandbox").write_bytes(b"old")
            (root / "etc/yoloctl").mkdir(parents=True)
            (root / "etc/yoloctl/sandbox-policy.rego").write_bytes(b"old")
            (root / "opt/keep").write_bytes(b"keep")
            builder.builder.drop_legacy_openshell(root)
            self.assertFalse((root / "opt/openshell").exists())
            self.assertFalse((root / "etc/yoloctl/sandbox-policy.rego").exists())
            self.assertEqual((root / "opt/keep").read_bytes(), b"keep")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "outside").mkdir()
            (root / "opt").symlink_to("/outside")
            with self.assertRaises(RuntimeError):
                builder.builder.drop_legacy_openshell(root)

    def test_absolute_guest_link_stays_inside_image(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'lib').mkdir()
            (root / 'lib/loader').write_bytes(b'guest')
            (root / 'bin').mkdir()
            (root / 'bin/loader').symlink_to('/lib/loader')
            self.assertEqual(builder.guest_path(root, '/bin/loader'), root / 'lib/loader')

    def test_path_escape_and_link_loop_fail(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'escape').symlink_to('../outside')
            (root / 'loop').symlink_to('loop')
            for path in ['/escape', '/loop']:
                with self.subTest(path=path), self.assertRaises(RuntimeError):
                    builder.guest_path(root, path)

    def test_runtime_dependency_gate_is_not_best_effort(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(RuntimeError, 'missing'):
                builder.verify_runtime(Path(tmp))


@unittest.skipUnless(shutil.which('openssl'), 'subject hashes need openssl')
class TrustStoreTests(unittest.TestCase):
    """Mirrors the Go trust-store regeneration test on a staging tree."""

    def certificate(self, cn):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / 'cert.pem'
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes',
                            '-keyout', Path(tmp) / 'key.pem', '-out', out, '-days', '1', '-subj', '/CN=' + cn],
                           check=True, capture_output=True)
            return out.read_bytes()

    def subject_hash(self, data):
        with tempfile.NamedTemporaryFile() as f:
            f.write(data)
            f.flush()
            return subprocess.run(['openssl', 'x509', '-noout', '-subject_hash', '-in', f.name],
                                  check=True, capture_output=True, text=True).stdout.strip()

    def stage(self, tmp):
        root = Path(tmp) / 'stage'
        for name in ['usr/share/ca-certificates/mozilla', 'usr/local/share/ca-certificates', 'etc/ssl/certs', 'usr/lib/ssl']:
            (root / name).mkdir(parents=True)
        return root

    def test_regeneration_matches_fresh_installation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = self.stage(tmp)
            retired = self.certificate('Retired Root')
            (root / 'usr/share/ca-certificates/mozilla/Retired_Root.crt').write_bytes(retired)
            certs = root / 'etc/ssl/certs'
            (certs / 'Retired_Root.pem').symlink_to('/usr/share/ca-certificates/mozilla/Retired_Root.crt')
            (certs / (self.subject_hash(retired) + '.0')).symlink_to('Retired_Root.pem')
            (certs / 'ca-certificates.crt').write_bytes(retired)
            (root / 'etc/ca-certificates.conf').write_text('mozilla/Retired_Root.crt\n')
            (root / 'usr/lib/ssl/cert.pem').symlink_to('/etc/ssl/certs/ca-certificates.crt')
            builder.drop_managed_certificates(root)
            self.assertFalse((root / 'usr/share/ca-certificates/mozilla').exists())
            self.assertEqual(list(certs.iterdir()), [])

            # Package extraction, as in the Go test.
            kept, twin_a, twin_b = self.certificate('Kept Root'), self.certificate('Twin'), self.certificate('Twin')
            odd, no_newline = self.certificate('Odd Name'), self.certificate('No Newline').rstrip(b'\n')
            package = {'Kept_Root.crt': kept, 'Copy_Of_Kept.crt': kept, 'Twin_B.crt': twin_b, 'Twin_A.crt': twin_a,
                       'Odd (Name), CA.crt': odd, 'no_newline.crt': no_newline}
            mozilla = root / 'usr/share/ca-certificates/mozilla'
            mozilla.mkdir()
            for name, data in package.items():
                (mozilla / name).write_bytes(data)
            builder.rebuild_trust_store(root)

            order = ['Copy_Of_Kept.crt', 'Kept_Root.crt', 'Odd (Name), CA.crt', 'Twin_A.crt', 'Twin_B.crt', 'no_newline.crt']
            self.assertEqual((root / 'etc/ca-certificates.conf').read_text(),
                             builder.builder.CERTS_CONF_HEADER + ''.join('mozilla/' + name + '\n' for name in order))
            bundle = b''.join(package[name] if package[name].endswith(b'\n') else package[name] + b'\n' for name in order)
            self.assertEqual((certs / 'ca-certificates.crt').read_bytes(), bundle)
            self.assertFalse((certs / 'ca-certificates.crt').is_symlink())
            want = {
                'Copy_Of_Kept.pem': '/usr/share/ca-certificates/mozilla/Copy_Of_Kept.crt',
                'Kept_Root.pem': '/usr/share/ca-certificates/mozilla/Kept_Root.crt',
                'Odd_=Name=__CA.pem': '/usr/share/ca-certificates/mozilla/Odd (Name), CA.crt',
                'Twin_A.pem': '/usr/share/ca-certificates/mozilla/Twin_A.crt',
                'Twin_B.pem': '/usr/share/ca-certificates/mozilla/Twin_B.crt',
                'no_newline.pem': '/usr/share/ca-certificates/mozilla/no_newline.crt',
                self.subject_hash(kept) + '.0': 'Copy_Of_Kept.pem',
                self.subject_hash(odd) + '.0': 'Odd_=Name=__CA.pem',
                self.subject_hash(twin_a) + '.0': 'Twin_A.pem',
                self.subject_hash(twin_a) + '.1': 'Twin_B.pem',
                self.subject_hash(no_newline) + '.0': 'no_newline.pem',
            }
            got = {entry.name: os.readlink(entry) for entry in certs.iterdir() if entry.is_symlink()}
            self.assertEqual(got, want)
            self.assertEqual(os.readlink(root / 'usr/lib/ssl/cert.pem'), '/etc/ssl/certs/ca-certificates.crt')

    def test_unmanaged_content_and_host_links_are_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            for name, setup in [
                ('local CA', lambda root: (root / 'usr/local/share/ca-certificates/private.crt').write_bytes(b'x')),
                ('unmanaged file', lambda root: (root / 'etc/ssl/certs/ssl-cert-snakeoil.pem').write_bytes(b'x')),
                ('unmanaged link', lambda root: (root / 'etc/ssl/certs/other.pem').symlink_to('/etc/other.pem')),
                ('nested entry', lambda root: (root / 'etc/ssl/certs/java').mkdir()),
            ]:
                with self.subTest(name=name):
                    root = Path(tmp) / name.replace(' ', '-')
                    shutil.copytree(self.stage(Path(tmp) / ('base-' + name.replace(' ', '-'))), root, symlinks=True)
                    setup(root)
                    with self.assertRaises(RuntimeError):
                        builder.drop_managed_certificates(root)
            # A store directory that is a link must not lead regeneration to the host.
            outside = Path(tmp) / 'outside'
            outside.mkdir()
            (outside / 'keep.pem').write_bytes(b'host')
            root = self.stage(Path(tmp) / 'linked')
            shutil.rmtree(root / 'etc/ssl/certs')
            (root / 'etc/ssl/certs').symlink_to(outside)
            with self.assertRaises(RuntimeError):
                builder.drop_managed_certificates(root)
            (root / 'usr/share/ca-certificates/mozilla').mkdir(exist_ok=True)
            (root / 'usr/share/ca-certificates/mozilla/Root.crt').write_bytes(self.certificate('Root'))
            with self.assertRaises(RuntimeError):
                builder.rebuild_trust_store(root)
            self.assertEqual([entry.name for entry in outside.iterdir()], ['keep.pem'])

    def test_unsafe_package_content_is_refused(self):
        distrusted = self.certificate('Distrusted')
        der = subprocess.run(['openssl', 'x509', '-outform', 'DER'], input=distrusted, check=True, capture_output=True).stdout
        fingerprint = hashlib.sha256(der).hexdigest()
        builder.builder.DISTRUSTED_ROOTS[fingerprint] = 'test root'
        self.addCleanup(builder.builder.DISTRUSTED_ROOTS.pop, fingerprint)
        with tempfile.TemporaryDirectory() as tmp:
            for name, files in [
                ('no certificates', {}),
                ('distrusted root', {'Distrusted.crt': distrusted}),
                ('two certificates', {'Pair.crt': self.certificate('One') + self.certificate('Two')}),
                ('not a certificate', {'Key.crt': b'-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n'}),
            ]:
                with self.subTest(name=name):
                    root = self.stage(Path(tmp) / name.replace(' ', '-'))
                    for file, data in files.items():
                        (root / 'usr/share/ca-certificates/mozilla' / file).write_bytes(data)
                    with self.assertRaises(RuntimeError):
                        builder.rebuild_trust_store(root)
            root = self.stage(Path(tmp) / 'leftover')
            (root / 'usr/share/ca-certificates/mozilla/Root.crt').write_bytes(self.certificate('Root'))
            (root / 'etc/ssl/certs/Old.pem').symlink_to('/usr/share/ca-certificates/mozilla/Old.crt')
            with self.assertRaises(RuntimeError):
                builder.rebuild_trust_store(root)


if __name__ == '__main__':
    unittest.main()
