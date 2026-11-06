"""push-main-checked.sh against a real bare remote and a scripted fake gh."""
from pathlib import Path
import os
import shutil
import subprocess
import tempfile
import textwrap
import unittest

SCRIPT = Path(__file__).with_name('push-main-checked.sh')
REPO = 'leogr/fixture'

# The fake answers only the gh calls the script makes, and records each call
# with its --repo value. A run exists for the commit currently on the remote
# pre-main; its checkout log merges it into the remote main (or FAKE_GH_LOG_BASE).
FAKE_GH = textwrap.dedent('''\
    #!/usr/bin/env python3
    import os, subprocess, sys
    args = sys.argv[1:]
    state = os.environ['FAKE_GH_STATE']
    repo = args[args.index('--repo') + 1] if '--repo' in args else '-'
    with open(os.path.join(state, 'calls'), 'a') as out:
        out.write((' '.join(args[:4:3]) if args[:1] == ['api'] else ' '.join(args[:2])) + ' repo=' + repo + ' GH_REPO=' + os.environ.get('GH_REPO', '-')
                  + ' GH_HOST=' + os.environ.get('GH_HOST', '-') + '\\n')
    def ref(name):
        return subprocess.run(['git', '-C', os.environ['FAKE_GH_REMOTE'], 'rev-parse', name],
                              capture_output=True, text=True).stdout.strip()
    joined = ' '.join(args)
    conclusion = os.environ.get('FAKE_GH_CONCLUSION', 'success')
    if args[:2] == ['auth', 'status']:
        sys.exit(0)
    if args[:2] == ['pr', 'list']:
        pr = os.path.join(state, 'pr')
        print(open(pr).read().strip() if os.path.exists(pr) else '')
        sys.exit(0)
    if args[:2] == ['pr', 'create']:
        open(os.path.join(state, 'pr'), 'w').write('17 true')
        sys.exit(0)
    if args[:2] == ['run', 'list']:
        print('42' if ref('pre-main') else '')
        sys.exit(0)
    if args[:2] == ['run', 'watch']:
        hook = os.environ.get('FAKE_GH_DURING_WATCH')
        if hook:
            subprocess.run(['sh', '-c', hook], check=True)
        sys.exit(0 if conclusion == 'success' else 1)
    if args[:2] == ['run', 'view'] and '--log' in args:
        head, base = ref('pre-main'), ref('main')
        tree = ref(os.environ.get('FAKE_GH_TESTED_TREE_OF') or head) + '^{tree}'
        merge = subprocess.run(['git', '-C', os.environ['FAKE_GH_REMOTE'], 'commit-tree', ref(tree),
                                '-p', base, '-p', head, '-m', 'Merge ' + head + ' into ' + base],
                               capture_output=True, text=True, check=True,
                               env=dict(os.environ, GIT_AUTHOR_NAME='CI', GIT_AUTHOR_EMAIL='ci@example.invalid',
                                        GIT_COMMITTER_NAME='CI', GIT_COMMITTER_EMAIL='ci@example.invalid')).stdout.strip()
        for job in ('build (ubuntu-latest)', 'build (macos-latest)', 'lint'):
            step = job + '\\tRun actions/checkout@v5\\t2026-11-06T14:00:00.0Z '
            print(step + 'HEAD is now at ' + merge[:7] + ' Merge ' + head + ' into ' + base)
            evidence = os.environ.get('FAKE_GH_EVIDENCE', 'one')
            if evidence != 'missing':
                print(step + "[command]/usr/bin/git log -1 --format='%H'")
                print(step + "'" + merge + "'")
            if evidence == 'ambiguous':
                print(step + "[command]/usr/bin/git log -1 --format='%H'")
                print(step + "'" + head + "'")
            print(job + '\\tRun tests\\t2026-11-06T14:01:00.0Z ' + 'f' * 40)
        sys.exit(0)
    if args and args[0] == 'api':
        if args[1:3] != ['--hostname', 'github.com']:
            sys.exit('api without --hostname github.com')
        path = args[3]
        if '/actions/runs/' in path:
            print('0' if os.environ.get('FAKE_GH_UNLINKED') else '1')
        else:
            print(ref(path.rsplit('/', 1)[-1] + '^{tree}'))
        sys.exit(0)
    if args[:2] == ['run', 'view'] and 'headSha,headBranch,event,conclusion' in joined:
        print(ref('pre-main') + ' pre-main pull_request ' + conclusion)
        sys.exit(0)
    if args[:2] == ['run', 'view'] and 'conclusion == "success"' in joined:
        print(os.environ.get('FAKE_GH_OK_JOBS', 'build (ubuntu-latest)\\nbuild (macos-latest)\\nlint'))
        sys.exit(0)
    if args[:2] == ['run', 'view'] and 'conclusion != "success"' in joined:
        print('  build (macos-latest): failure  https://example.invalid/job/1')
        sys.exit(0)
    sys.exit('unexpected gh call: ' + joined)
''')

# A git wrapper whose final push to main is applied by the server but reported
# as failed, as when the reply is lost.
LOSSY_GIT = textwrap.dedent('''\
    #!/bin/sh
    case "$*" in
    *push*refs/heads/main*) "$REAL_GIT" "$@" >/dev/null 2>&1; echo "fatal: the remote end hung up" >&2; exit 128 ;;
    esac
    exec "$REAL_GIT" "$@"
''')


def git(cwd, *args):
    return subprocess.run(['git', '-C', str(cwd), *args], check=True, capture_output=True, text=True).stdout.strip()


class PushMainCheckedTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.root = root
        self.remote, self.work, self.state, self.bin = root / 'remote.git', root / 'work', root / 'state', root / 'bin'
        for d in (self.state, self.bin):
            d.mkdir()
        (self.bin / 'gh').write_text(FAKE_GH)
        (self.bin / 'gh').chmod(0o755)
        subprocess.run(['git', 'init', '-q', '--bare', '-b', 'main', str(self.remote)], check=True)
        subprocess.run(['git', 'init', '-q', '-b', 'main', str(self.work)], check=True)
        for key, value in (('user.name', 'Test'), ('user.email', 'test@example.invalid'), ('commit.gpgsign', 'false')):
            git(self.work, 'config', key, value)
        git(self.work, 'remote', 'add', 'origin', str(self.remote))
        self.commit('base')
        git(self.work, 'push', '-q', 'origin', 'main')
        self.base = git(self.work, 'rev-parse', 'main')
        self.env = dict(os.environ, PATH=f'{self.bin}{os.pathsep}{os.environ["PATH"]}',
                        FAKE_GH_STATE=str(self.state), FAKE_GH_REMOTE=str(self.remote),
                        PUSH_MAIN_TEST_REPO=REPO, PUSH_MAIN_POLL_SECONDS='0', PUSH_MAIN_APPEAR_TRIES='3')

    def commit(self, name):
        (self.work / name).write_text(name)
        git(self.work, 'add', name)
        git(self.work, 'commit', '-q', '-m', name)
        return git(self.work, 'rev-parse', 'HEAD')

    def run_script(self, **env):
        return subprocess.run(['bash', str(SCRIPT)], cwd=self.work, env=dict(self.env, **env),
                              capture_output=True, text=True, timeout=60)

    def remote_ref(self, ref):
        return subprocess.run(['git', '-C', str(self.remote), 'rev-parse', '--verify', '--quiet', ref],
                              capture_output=True, text=True).stdout.strip()

    def calls(self):
        path = self.state / 'calls'
        return path.read_text() if path.exists() else ''

    def assert_main_untouched(self, result):
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn('This script did not push main', result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/main'), self.base)

    def test_green_run_fast_forwards_main_without_tags(self):
        head = self.commit('change')
        git(self.work, 'tag', 'v9.9.9')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/main'), head)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), head)
        self.assertEqual(git(self.remote, 'tag', '--list'), '')
        self.assertIn('pr create', self.calls())

    def test_every_gh_call_names_the_remote_repository(self):
        self.commit('change')
        result = self.run_script(GH_REPO='someone/else', GH_HOST='evil.example')
        self.assertEqual(result.returncode, 0, result.stderr)
        for line in self.calls().splitlines():
            self.assertIn('GH_HOST=github.com', line)
            if line.startswith('api '):
                self.assertTrue(line.startswith('api repos/' + REPO + '/'), line)
            elif not line.startswith('auth status'):
                self.assertIn('repo=' + REPO, line)
            self.assertIn('GH_REPO=' + REPO, line)

    def test_separate_push_url_is_refused_before_any_push(self):
        elsewhere = self.root / 'elsewhere.git'
        subprocess.run(['git', 'init', '-q', '--bare', str(elsewhere)], check=True)
        git(self.work, 'config', 'remote.origin.pushurl', str(elsewhere))
        self.commit('change')
        result = self.run_script()
        self.assert_main_untouched(result)
        self.assertIn('use one URL', result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), '')
        self.assertEqual(self.calls(), '')

    def test_non_github_remote_without_a_named_repository_is_refused(self):
        self.commit('change')
        env = dict(self.env)
        del env['PUSH_MAIN_TEST_REPO']
        result = subprocess.run(['bash', str(SCRIPT)], cwd=self.work, env=env, capture_output=True, text=True, timeout=60)
        self.assert_main_untouched(result)
        self.assertIn('not a github.com repository', result.stderr)

    def test_existing_draft_pull_request_is_reused(self):
        (self.state / 'pr').write_text('5 true')
        head = self.commit('change')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn('pr create', self.calls())
        self.assertIn('#5', result.stdout)
        self.assertEqual(self.remote_ref('refs/heads/main'), head)

    def test_non_draft_or_duplicate_pull_requests_are_refused(self):
        self.commit('change')
        for prs, message in (('5 false', 'not a draft'), ('5 true\n6 true', 'more than one')):
            with self.subTest(prs=prs):
                (self.state / 'pr').write_text(prs)
                result = self.run_script()
                self.assert_main_untouched(result)
                self.assertIn(message, result.stderr)

    def test_red_run_lists_failing_jobs_and_leaves_main(self):
        self.commit('change')
        result = self.run_script(FAKE_GH_CONCLUSION='failure')
        self.assert_main_untouched(result)
        self.assertIn('build (macos-latest): failure', result.stderr)

    def test_green_run_without_the_macos_build_job_is_refused(self):
        self.commit('change')
        result = self.run_script(FAKE_GH_OK_JOBS='build (ubuntu-latest)\nlint\nmacos-something')
        self.assert_main_untouched(result)
        self.assertIn('no passing job "build (macos-latest)"', result.stderr)

    def test_stale_run_whose_checkout_has_another_tree_is_refused(self):
        # A run of the same head merged into another base checks out another tree.
        self.commit('change')
        result = self.run_script(FAKE_GH_TESTED_TREE_OF='main')
        self.assert_main_untouched(result)
        self.assertIn('cannot vouch', result.stderr)

    def test_run_not_linked_to_the_draft_pull_request_is_refused(self):
        self.commit('change')
        result = self.run_script(FAKE_GH_UNLINKED='1')
        self.assert_main_untouched(result)
        self.assertIn('not linked to draft pull request', result.stderr)

    def test_unreadable_main_after_a_failed_final_push_is_uncertain(self):
        self.commit('change')
        wrapper = self.bin / 'git'
        wrapper.write_text(LOSSY_GIT.replace('*push*refs/heads/main*)', '*ls-remote*refs/heads/main*) [ -f "$LOSSY_LS" ] && exit 2 ;;\n*push*refs/heads/main*) : > "$LOSSY_LS";'))
        wrapper.chmod(0o755)
        result = self.run_script(REAL_GIT=shutil.which('git'), LOSSY_LS=str(self.state / 'pushed'))
        self.assertEqual(result.returncode, 1)
        self.assertIn('could not be read back: its state is uncertain', result.stderr)

    def test_missing_or_ambiguous_checkout_evidence_is_refused(self):
        self.commit('change')
        for evidence in ('missing', 'ambiguous'):
            with self.subTest(evidence=evidence):
                result = self.run_script(FAKE_GH_EVIDENCE=evidence)
                self.assert_main_untouched(result)
                self.assertIn('no single checked-out commit', result.stderr)

    def test_main_moving_during_the_run_is_refused(self):
        self.commit('change')
        other = self.state / 'other'
        hook = (f'git clone -q {self.remote} {other} && cd {other} && git config user.name T && '
                'git config user.email t@example.invalid && git commit -q --allow-empty -m moved && '
                'git push -q origin HEAD:main')
        result = self.run_script(FAKE_GH_DURING_WATCH=hook)
        self.assertEqual(result.returncode, 1)
        self.assertIn('This script did not push main', result.stderr)
        self.assertNotEqual(self.remote_ref('refs/heads/main'), git(self.work, 'rev-parse', 'main'))

    def test_lost_reply_on_the_final_push_is_reported_uncertain(self):
        head = self.commit('change')
        wrapper = self.bin / 'git'
        wrapper.write_text(LOSSY_GIT)
        wrapper.chmod(0o755)
        result = self.run_script(REAL_GIT=shutil.which('git'))
        self.assertEqual(result.returncode, 1)
        self.assertIn('reported a failure; nothing was retried', result.stderr)
        self.assertIn('Remote main now equals the intended commit ' + head, result.stderr)
        self.assertNotIn('did not push main', result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/main'), head)

    def test_non_fast_forward_is_refused_before_any_push(self):
        other = self.state / 'other'
        subprocess.run(['git', 'clone', '-q', str(self.remote), str(other)], check=True)
        for key, value in (('user.name', 'T'), ('user.email', 't@example.invalid')):
            git(other, 'config', key, value)
        git(other, 'commit', '-q', '--allow-empty', '-m', 'elsewhere')
        git(other, 'push', '-q', 'origin', 'HEAD:main')
        self.commit('change')
        result = self.run_script()
        self.assertEqual(result.returncode, 1)
        self.assertIn('not a fast-forward', result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), '')

    def test_rerun_after_red_moves_pre_main_under_its_lease(self):
        first = self.commit('first')
        self.assertEqual(self.run_script(FAKE_GH_CONCLUSION='failure').returncode, 1)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), first)
        fixed = self.commit('fix')
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), fixed)
        self.assertEqual(self.remote_ref('refs/heads/main'), fixed)

    def test_invalid_polling_settings_are_refused(self):
        self.commit('change')
        for env in ({'PUSH_MAIN_POLL_SECONDS': 'soon'}, {'PUSH_MAIN_APPEAR_TRIES': '0'}):
            with self.subTest(env=env):
                result = self.run_script(**env)
                self.assert_main_untouched(result)
                self.assertEqual(self.calls(), '')

    def test_nothing_to_push(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('nothing to push', result.stdout)
        self.assertEqual(self.remote_ref('refs/heads/pre-main'), '')


if __name__ == '__main__':
    unittest.main()
