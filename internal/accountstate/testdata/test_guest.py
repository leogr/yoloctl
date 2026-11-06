"""Exercise real guest filesystem operations on disposable directories.

Only findmnt and root ownership are substituted: no mounts or privilege needed.
"""
import base64
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('guest', sys.argv.pop(1))
guest = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guest)


class NativeStorage(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.disk = Path(self.tmp.name)
        self.root = self.disk / 'account-state'
        self.req = dict(action='initialize', mount=str(self.disk), root=str(self.root),
                        id='a' * 32, uuid='', uid=os.getuid(), gid=os.getgid())
        self.mount = patch.object(guest.subprocess, 'check_output', return_value=json.dumps(
            {'filesystems': [dict(fstype='ext4', uuid='native-uuid')]}).encode()).start()
        # Tests run unprivileged; disk marker ownership is tested in live smoke.
        original_fchown, original_chown = os.fchown, os.chown
        patch.object(guest.os, 'fchown', side_effect=lambda fd, uid, gid:
                     original_fchown(fd, os.getuid(), os.getgid())).start()
        patch.object(guest.os, 'chown', side_effect=lambda path, uid, gid, **kw:
                     original_chown(path, os.getuid(), os.getgid(), **kw)).start()
        self.addCleanup(patch.stopall)
        self.assertEqual(guest.run(self.req)['uuid'], 'native-uuid')
        self.req.update(uuid='native-uuid', account='personal', agent='codex')

    def runop(self, action, **kw):
        return guest.run(dict(self.req, action=action, **kw))

    def test_verify_never_creates_or_repairs_a_missing_bind(self):
        missing = self.disk / 'missing-bind'
        with patch.object(guest.subprocess, 'run') as mount:
            with self.assertRaises(FileNotFoundError):
                self.runop('verify', root=str(missing), uuid='native-uuid')
            mount.assert_not_called()
        self.assertFalse(missing.exists())

        wrong = self.disk / 'wrong-bind'
        wrong.mkdir(mode=0o700)
        with patch.object(guest.subprocess, 'run') as mount:
            with self.assertRaisesRegex(ValueError, 'mount is unavailable'):
                self.runop('verify', root=str(wrong), uuid='native-uuid')
            mount.assert_not_called()
        self.assertEqual(list(wrong.iterdir()), [])

    def test_import_status_remove(self):
        self.assertFalse(self.runop('exists', path='auth.json')['present'])
        payload = b'{"token":"never-log-this"}'
        self.runop('write', path='sub/auth.json', data=base64.b64encode(payload).decode())
        leaf = self.root / 'personal' / 'codex'
        self.assertEqual((leaf / 'sub/auth.json').read_bytes(), payload)
        self.assertEqual((leaf / 'sub/auth.json').stat().st_mode & 0o777, 0o600)
        self.assertEqual(leaf.stat().st_mode & 0o777, 0o700)
        self.assertTrue(self.runop('exists', path='sub/auth.json')['present'])
        self.assertEqual(self.runop('list')['leaves'], ['codex'])
        self.runop('remove')
        self.assertFalse((self.root / 'personal').exists())
        self.runop('remove')  # retry after a partial destroy

    def auth(self, fmt='claude', profiles='anthropic'):
        spec = dict(format=fmt, profiles_dir=profiles)
        path = '.credentials.json' if fmt == 'claude' else 'auth.json'
        return self.runop('auth-status', path=path,
                          data=base64.b64encode(json.dumps(spec).encode()).decode())['auth']

    def put(self, path, data):
        if not isinstance(data, bytes):
            data = json.dumps(data).encode()
        self.runop('write', path=path, data=base64.b64encode(data).decode())

    def test_auth_material_is_classified_without_returning_values(self):
        self.assertEqual([x['outcome'] for x in self.auth()], ['missing', 'missing'])
        for fmt, filename, good in [
                ('claude', '.credentials.json', {'claudeAiOauth': {'accessToken': 'synthetic-access'}}),
                ('codex', 'auth.json', {'tokens': {'access_token': 'synthetic-access'}}),
                ('codex', 'auth.json', {'OPENAI_API_KEY': 'synthetic-api'})]:
            for body, expected in [(b'{}', 'invalid'), (b' ', 'invalid'), (b'not-json', 'invalid'),
                                   (b'[]', 'invalid'), ({'unknown': 'synthetic-access'}, 'unverified'),
                                   (good, 'credentials-present')]:
                with self.subTest(fmt=fmt, body_type=type(body).__name__):
                    self.put(filename, body)
                    result = self.auth(fmt, '')
                    self.assertEqual(result[0]['outcome'], expected)
                    self.assertNotIn('synthetic', json.dumps(result))
            self.put(filename, b' ' * (guest.SETTINGS_BYTES + 1))
            self.assertEqual(self.auth(fmt, '')[0]['outcome'], 'invalid')
        self.put('auth.json', b'{"OPENAI_API_KEY":"synthetic-api","OPENAI_API_KEY":""}')
        self.assertEqual(self.auth('codex', '')[0]['outcome'], 'invalid')

    def test_anthropic_active_profile_and_config_are_required(self):
        self.put('.credentials.json', {})
        self.put('anthropic/active_config', b'work\n')
        self.put('anthropic/configs/work.json', {'version': '1', 'authentication': {'type': 'user_oauth'}})
        self.put('anthropic/credentials/work.json', {'version': '1', 'type': 'oauth_token', 'access_token': 'synthetic-access'})
        self.assertEqual([x['outcome'] for x in self.auth()], ['invalid', 'credentials-present'])
        self.put('anthropic/configs/work.json', {'authentication': {'type': 'workload_identity'}})
        self.assertEqual(self.auth()[1]['outcome'], 'external')
        # External identity configuration is still external without a cached token.
        cached = self.root / 'personal/codex/anthropic/credentials/work.json'
        cached.unlink()
        self.assertEqual(self.auth()[1]['outcome'], 'external')
        self.put('anthropic/active_config', b'missing\n')
        self.assertEqual(self.auth()[1]['outcome'], 'missing')
        self.put('anthropic/active_config', b'../outside')
        self.assertEqual(self.auth()[1]['outcome'], 'invalid')

    def test_auth_reads_and_state_subdirs_fail_on_links_and_access_errors(self):
        self.put('anthropic/configs/default.json', {})
        leaf = self.root / 'personal' / 'codex'
        self.assertEqual((leaf / 'anthropic/configs').stat().st_mode & 0o777, 0o700)
        outside = self.disk / 'outside'
        outside.mkdir()
        (leaf / 'anthropic/credentials').symlink_to(outside, target_is_directory=True)
        with self.assertRaises(OSError):
            self.put('anthropic/credentials/default.json', {})
        with self.assertRaises(OSError):
            self.auth()
        with patch.object(guest, 'regular', side_effect=PermissionError('native read denied')):
            with self.assertRaises(PermissionError):
                self.auth()

    def test_auth_io_errors_do_not_echo_selected_names(self):
        self.runop('mkdir')
        original = guest.regular
        def denied(fd, name):
            if name == '.credentials.json':
                raise PermissionError(13, 'denied', 'synthetic-private-material')
            return original(fd, name)
        with patch.object(guest, 'regular', side_effect=denied):
            with self.assertRaises(OSError) as error:
                self.auth()
        self.assertNotIn('synthetic-private', str(error.exception))

    def test_new_state_is_removed_with_the_account_leaf(self):
        self.put('.credentials.json', b'legacy-bytes')
        self.put('anthropic/credentials/default.json', b'synthetic-new-bytes')
        leaf = self.root / 'personal' / 'codex'
        self.assertEqual((leaf / '.credentials.json').read_bytes(), b'legacy-bytes')
        self.runop('remove')
        self.assertFalse(leaf.exists())

    def test_imported_profile_directories_belong_to_the_account_user(self):
        with patch.object(guest.os, 'chown', wraps=guest.os.chown) as chown:
            self.put('anthropic/configs/default.json', {'authentication': {'type': 'user_oauth'}})
            self.put('anthropic/credentials/default.json', {'type': 'oauth_token', 'access_token': 'synthetic-access'})
        created = {call.args[0]: call.args[1:3] for call in chown.call_args_list}
        for name in ('anthropic', 'configs', 'credentials'):
            self.assertEqual(created[name], (self.req['uid'], self.req['gid']))

    def test_disk_identity_fails_closed(self):
        for changes in [dict(uuid='wrong'), dict(id='b' * 32)]:
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                self.runop('mkdir', **changes)
        self.assertFalse((self.root / 'personal').exists())
        (self.disk / '.yoloctl-owner').unlink()
        with self.assertRaises(ValueError):
            self.runop('prepare')
        self.assertFalse((self.disk / '.yoloctl-owner').exists())

    def test_unmounted_or_wrong_filesystem_has_no_fallback(self):
        for fs in [dict(fstype='9p', uuid='native-uuid'), dict(fstype='ext4', uuid='')]:
            self.mount.return_value = json.dumps({'filesystems': [fs]}).encode()
            with self.assertRaises(ValueError):
                self.runop('mkdir')
        self.assertFalse((self.root / 'personal').exists())

    def test_bind_must_be_the_declared_disk(self):
        other = self.disk / 'empty-fallback'
        other.mkdir()
        with self.assertRaises(ValueError):
            self.runop('write', root=str(other), path='auth.json', data='eA==')
        self.assertEqual(list(other.iterdir()), [])

    def test_account_and_nested_symlinks_are_not_followed(self):
        outside = self.disk / 'outside'
        outside.mkdir()
        victim = outside / 'auth.json'
        victim.write_bytes(b'preserve')
        acct = self.root / 'personal'
        acct.symlink_to(outside, target_is_directory=True)
        for action in ['remove', 'mkdir', 'list']:
            with self.subTest(action=action), self.assertRaises(OSError):
                self.runop(action)
        acct.unlink()
        self.runop('mkdir')
        leaf = acct / 'codex'
        (leaf / 'nested').symlink_to(outside, target_is_directory=True)
        for action in ['write', 'exists']:
            with self.subTest(action=action), self.assertRaises(OSError):
                self.runop(action, path='nested/auth.json', data='eA==')
        (leaf / 'auth.json').symlink_to(victim)
        with self.assertRaises(OSError):
            self.runop('exists', path='auth.json')
        # Atomic replacement may replace the link itself; never its target.
        self.runop('write', path='auth.json', data='eA==')
        self.assertEqual(victim.read_bytes(), b'preserve')
        self.runop('remove')
        self.assertEqual(victim.read_bytes(), b'preserve')

    def test_traversal_rejected(self):
        for path in ['../auth.json', '/auth.json', 'sub/../../auth.json']:
            with self.subTest(path=path), self.assertRaises(ValueError):
                self.runop('write', path=path, data='eA==')
        self.assertFalse((self.root / 'auth.json').exists())


    def status_line(self, enable, command='/usr/local/libexec/yoloctl/agent-statusline-agy'):
        data = base64.b64encode(json.dumps({'enable': enable, 'command': command}).encode()).decode()
        return self.runop('statusline', agent='agy', path='antigravity-cli/settings.json', data=data).get('outcome')

    def test_status_line_is_set_and_restored_keeping_other_keys(self):
        ours = '/usr/local/libexec/yoloctl/agent-statusline-agy'
        settings = self.root / 'personal/agy/antigravity-cli/settings.json'
        stash = settings.parent / 'yoloctl-statusline.json'
        # No settings yet: nothing is created, not even the leaf; the
        # launcher seeds the account with its defaults at the first start.
        self.assertEqual(self.status_line(True), 'absent')
        self.assertFalse((self.root / 'personal').exists())
        self.runop('write', agent='agy', path='antigravity-cli/settings.json', data=base64.b64encode(b'{}').decode())
        self.assertEqual(self.status_line(True), 'enabled')
        self.assertEqual(json.loads(settings.read_text()),
                         {'statusLine': {'type': 'command', 'command': ours, 'stack_with_default': True}})
        self.assertEqual(json.loads(stash.read_text()), {'v': 1, 'previous': None})
        self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.status_line(True), 'unchanged')
        self.assertEqual(self.status_line(False), 'removed')
        self.assertEqual(json.loads(settings.read_text()), {})
        self.assertFalse(stash.exists())
        settings.unlink()
        self.assertEqual(self.status_line(True), 'absent')
        self.assertFalse(settings.exists() or stash.exists(), 'a missing file is never created')
        self.assertEqual(self.status_line(False), 'unchanged')
        # The user's own line: replaced with its layout, saved verbatim, restored.
        theirs = {'type': 'command', 'command': '~/bin/line.sh', 'padding': 1, 'stack_with_default': False}
        settings.write_text(json.dumps({'model': 'gemini', 'statusLine': theirs, 'trustedWorkspaces': ['/w']}))
        self.assertEqual(self.status_line(True), 'enabled')
        got = json.loads(settings.read_text())
        self.assertEqual(got['statusLine'], {'type': 'command', 'command': ours, 'padding': 1, 'stack_with_default': False})
        self.assertEqual((got['model'], got['trustedWorkspaces']), ('gemini', ['/w']))
        self.assertEqual(json.loads(stash.read_text()), {'v': 1, 'previous': theirs})
        self.assertEqual(self.status_line(False), 'restored')
        self.assertEqual(json.loads(settings.read_text()), {'model': 'gemini', 'statusLine': theirs, 'trustedWorkspaces': ['/w']})
        # A disabled user line is kept but not chained: agy's line stays.
        off = {'command': 'x', 'enabled': False}
        settings.write_text(json.dumps({'statusLine': off}))
        self.status_line(True)
        self.assertTrue(json.loads(settings.read_text())['statusLine']['stack_with_default'])
        self.status_line(False)
        self.assertEqual(json.loads(settings.read_text()), {'statusLine': off})
        # A line changed by hand since enabling is the user's: not ours to restore.
        self.status_line(True)
        settings.write_text(json.dumps({'statusLine': {'command': 'mine'}}))
        self.assertEqual(self.status_line(False), 'unchanged')
        self.assertEqual(json.loads(settings.read_text()), {'statusLine': {'command': 'mine'}})

    def test_status_line_refuses_what_it_cannot_keep(self):
        self.runop('write', agent='agy', path='antigravity-cli/settings.json', data=base64.b64encode(b'{}').decode())
        self.status_line(True)
        settings = self.root / 'personal/agy/antigravity-cli/settings.json'
        before = settings.read_bytes()
        for content in ('{"a":1,"a":2}', '[]', '{"a":NaN}', '{', ' ' * (1 << 20) + '{}'):
            settings.write_text(content)
            with self.subTest(content=content[:20]), self.assertRaises(ValueError):
                self.status_line(False)
            self.assertEqual(settings.read_text(), content, 'nothing is rewritten')
        settings.write_bytes(before)
        (settings.parent / 'yoloctl-statusline.json').write_text('{"v":2}')
        with self.assertRaises(ValueError):
            self.status_line(False)
        self.assertEqual(settings.read_bytes(), before)
        # Links are never followed, for the settings or the saved line.
        outside = self.disk / 'outside.json'
        outside.write_text('{}')
        settings.unlink()
        settings.symlink_to(outside)
        with self.assertRaises(OSError):
            self.status_line(True)
        self.assertEqual(outside.read_text(), '{}')
        # An account that never existed changes nothing either way.
        self.req.update(account='other')
        self.assertEqual((self.status_line(False), self.status_line(True)), ('absent', 'absent'))
        self.assertFalse((self.root / 'other').exists())


unittest.main()
