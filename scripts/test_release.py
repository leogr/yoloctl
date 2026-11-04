"""Offline release privilege, tag and artifact contract regressions."""
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import unittest
import urllib.error
import zipfile

spec = importlib.util.spec_from_file_location('publish_release', Path(__file__).with_name('publish-release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def environment():
    return dict(VERSION='v1.2.3-qa.1', GITHUB_EVENT_NAME='push', GITHUB_REF='refs/tags/v1.2.3-qa.1',
                SOURCE_SHA='a' * 40, GITHUB_REPOSITORY='leogr/yoloctl', GH_TOKEN='fixture-only',
                ARTIFACT_ID='42', GITHUB_RUN_ID='99', ARTIFACT_DIGEST='b' * 64)


class ReleaseTests(unittest.TestCase):
    def test_strict_tag_only_identity(self):
        env = environment()
        self.assertEqual(release.identity(env), env['VERSION'])
        for version in ('dev', 'v1.2', 'v01.2.3', 'v1.2.3-01', 'v1.2.3+meta', 'v1.2.3\n'):
            with self.subTest(version=version), self.assertRaises(ValueError):
                release.identity(dict(env, VERSION=version, GITHUB_REF='refs/tags/' + version))
        for changes in ({'GITHUB_EVENT_NAME': 'workflow_dispatch'}, {'GITHUB_EVENT_NAME': 'pull_request'},
                        {'GITHUB_REF': 'refs/heads/main'}, {'SOURCE_SHA': 'main'}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                release.identity(dict(env, **changes))

    def test_collision_and_network_failure_are_not_absence(self):
        api = release.GitHub(environment())
        for draft in (True, False):
            api.request = lambda path: [{'tag_name': 'v1.2.3', 'draft': draft}]
            with self.assertRaises(ValueError):
                api.no_release('v1.2.3')
        for status in (403, 404, 500):
            def request(path):
                raise urllib.error.HTTPError('https://api.github.com', status, 'error', {}, None)
            api.request = request
            with self.assertRaises(urllib.error.HTTPError):
                api.no_release('v1.2.3')
        api.request = lambda path: []
        api.no_release('v1.2.3')
        seen = []
        def paged(path):
            seen.append(path)
            if path.endswith('page=1'):
                return [{'tag_name': 'other'}] * 100
            return [{'tag_name': 'v1.2.3', 'draft': True}]
        api.request = paged
        with self.assertRaises(ValueError):
            api.no_release('v1.2.3')
        self.assertEqual(len(seen), 2)

    def test_tag_must_resolve_to_gated_commit(self):
        api = release.GitHub(environment())
        api.request = lambda path: {'object': {'type': 'commit', 'sha': 'b' * 40}}
        with self.assertRaises(ValueError):
            api.exact_tag('v1.2.3', 'a' * 40)
        api.request = lambda path: {'object': {'type': 'commit', 'sha': 'a' * 40}}
        api.exact_tag('v1.2.3', 'a' * 40)

    def test_gated_artifact_id_run_digest_and_entries(self):
        env = environment()
        version = env['VERSION']
        payload = io.BytesIO()
        with zipfile.ZipFile(payload, 'w') as z:
            for name in ['SHA256SUMS'] + [f'yoloctl-{osname}-{arch}.tar.gz'
                                        for osname in ('linux', 'darwin') for arch in ('amd64', 'arm64')]:
                z.writestr(name, b'fixture')
        raw = payload.getvalue()
        env['ARTIFACT_DIGEST'] = hashlib.sha256(raw).hexdigest()
        meta = dict(name='release-bundles', expired=False, workflow_run={'id': 99}, digest='sha256:' + env['ARTIFACT_DIGEST'])
        class API:
            def request(self, path):
                self.path = path
                return meta
            def artifact_zip(self, artifact_id, path):
                assert artifact_id == '42'
                path.write_bytes(raw)
        for mismatch in (None, 'run', 'digest', 'expired'):
            with self.subTest(mismatch=mismatch), tempfile.TemporaryDirectory() as tmp:
                api = API()
                if mismatch == 'run':
                    meta['workflow_run']['id'] = 100
                elif mismatch == 'digest':
                    meta['digest'] = 'sha256:' + 'c' * 64
                elif mismatch == 'expired':
                    meta['expired'] = True
                else:
                    meta['workflow_run']['id'] = 99
                    meta['digest'] = 'sha256:' + env['ARTIFACT_DIGEST']
                if mismatch:
                    with self.assertRaises(ValueError):
                        release.verify_artifact(api, env, version, Path(tmp) / 'archive.zip', Path(tmp) / 'dist')
                else:
                    release.verify_artifact(api, env, version, Path(tmp) / 'archive.zip', Path(tmp) / 'dist')
                    self.assertEqual(len(list((Path(tmp) / 'dist').iterdir())), 5)
                    self.assertEqual(api.path, '/actions/artifacts/42')
                    final_env = dict(env, RUNNER_TEMP=tmp)
                    (Path(tmp) / 'archive.zip').rename(Path(tmp) / 'release-artifact.zip')
                    release.verify_final_bytes(final_env, version, Path(tmp) / 'dist')
                    (Path(tmp) / 'dist/SHA256SUMS').write_bytes(b'changed after attestation')
                    with self.assertRaises(ValueError):
                        release.verify_final_bytes(final_env, version, Path(tmp) / 'dist')
                meta.update(name='release-bundles', expired=False, workflow_run={'id': 99}, digest='sha256:' + env['ARTIFACT_DIGEST'])
