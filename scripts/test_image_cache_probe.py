"""Exercise the image smoke's exact APT marker across normal lists cleanup.

The real apt update uses an unsigned, trusted local fixture repository only;
there is no network, host package state, installation, or production trust change.
"""
import hashlib
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest


SOURCE = Path(__file__).with_name('smoke') / 'image-ensure.sh'


def marker_script(lists):
    source = SOURCE.read_text()
    match = re.search(r'^RUN --mount=type=cache,id=yoloctl-apt-lists-v1,[^\n]*\n(.*?)(?=\nRUN )',
                      source, re.MULTILINE | re.DOTALL)
    if match is None:
        raise AssertionError('missing standalone APT lists retention probe')
    # The source is a shell heredoc producing Dockerfile RUN instructions.
    return (match[1].replace('\\$', '$').replace('\\\n', '')
            .replace('/var/lib/apt/lists', shlex.quote(str(lists))))


class ImageCacheProbeTests(unittest.TestCase):
    def probe(self, lists, phase):
        env = dict(os.environ, SMOKE_PHASE=phase, SMOKE_TOKEN='private-fixture-token')
        return subprocess.run(['sh', '-ceu', marker_script(lists)], env=env,
                              capture_output=True, text=True, timeout=10)

    def test_reset_cache_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix='apt-cache-reset-') as temp:
            result = self.probe(Path(temp), 'after')
            self.assertNotEqual(result.returncode, 0, 'a reset lists mount passed retention')

    @unittest.skipUnless(sys.platform.startswith('linux') and shutil.which('apt-get') and shutil.which('apt-config'),
                         'requires Linux apt for a local fixture update')
    def test_marker_survives_real_apt_update(self):
        with tempfile.TemporaryDirectory(prefix='apt-cache-update-') as temp:
            root = Path(temp)
            root.chmod(0o755)  # apt's download helper can read the fixture as root too.
            for directory in ('lists/partial', 'archives/partial', 'parts', 'repo', 'log'):
                (root / directory).mkdir(parents=True)
            (root / 'repo/Packages').write_text('')
            sources = root / 'sources.list'
            sources.write_text('deb [trusted=yes] file:' + str(root / 'repo') + ' ./\n')
            config = root / 'apt.conf'
            config.write_text('\n'.join([
                'Dir::Etc::main "/dev/null";',
                f'Dir::Etc::parts "{root / "parts"}";',
                f'Dir::Etc::sourceparts "{root / "parts"}";',
                f'Dir::Etc::sourcelist "{sources}";',
                f'Dir::State::lists "{root / "lists"}";',
                'Dir::State::status "/dev/null";',
                f'Dir::Cache::archives "{root / "archives"}";',
                'Dir::Cache::pkgcache "";', 'Dir::Cache::srcpkgcache "";',
                f'Dir::Log "{root / "log"}";', 'Acquire::Languages "none";',
            ]) + '\n')
            env = dict(os.environ, APT_CONFIG=str(config))
            dump = subprocess.check_output(['apt-config', 'dump'], env=env, text=True)
            self.assertNotIn('Post-Invoke', dump, 'host hooks must never execute')
            self.assertNotIn('List-Cleanup', dump, 'exercise normal APT lists cleanup')

            def update():
                result = subprocess.run(['apt-get', 'update', '--error-on=any'], env=env,
                                        capture_output=True, text=True, timeout=30)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

            def indexes():
                return {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                        for p in (root / 'lists').iterdir() if p.is_file() and p.name != 'lock'}

            update()
            before = indexes()
            self.assertTrue(before, 'fixture did not create actual apt index files')
            result = self.probe(root / 'lists', 'before')
            self.assertEqual(result.returncode, 0, result.stderr)
            # The former smoke failed here despite a successful unchanged-index
            # update: APT removes this unrecognized top-level marker by design.
            obsolete = root / 'lists/smoke-retained'
            obsolete.write_text('old-invalid-probe')
            update()
            self.assertFalse(obsolete.exists(), 'fixture did not exercise APT cleanup')
            self.assertEqual(indexes(), before, 'existing package indexes were not retained')
            result = self.probe(root / 'lists', 'after')
            self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
