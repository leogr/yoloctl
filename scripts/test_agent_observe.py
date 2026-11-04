"""Agent observation hook writer: existing-directory-only, bounded and silent."""
import errno
import fcntl
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


REPO = Path(__file__).resolve().parents[1]
WRITER = REPO / 'internal/features/agent-observe.py'
# Claude Code 2.1.311 payloads from the S0 capture, and the lines the writer
# must make of them (pid, ts and dur_ms normalized); the Go reader parses the
# same lines. They are facts of that version, not a contract.
VECTORS = REPO / 'internal/agentsession/observe/testdata/claude-2.1.311'
# D6/A+ captured Claude 2.1.312 payloads plus labelled input/dialog/failure fixtures.
# Private content is stripped; A+ session IDs use the shared fixture UUID.
CLAUDE_STATE_VECTORS = REPO / 'internal/agentsession/observe/testdata/claude-2.1.312-state'
# Synthetic Codex 0.164 payloads from the documented hook fields, until the
# stage 2 live check replaces them with a capture.
CODEX_VECTORS = REPO / 'internal/agentsession/observe/testdata/codex-0.164-synthetic'
spec = importlib.util.spec_from_file_location('agent_observe', WRITER)
agent_observe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(agent_observe)

SID = '0f8fad5b-d9cb-469f-a165-70867728950e'


class WriterBase(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / 'agent'
        self.gen = self.root / 'gen-3'
        self.gen.mkdir(parents=True, mode=0o700)
        for patcher in (patch.object(agent_observe, 'ROOT', str(self.root)),
                        patch.dict(os.environ, {'YOLOCTL_AGENT_GEN': '3'})):
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_hook(self, payload, args=()):
        raw = payload if isinstance(payload, bytes) else json.dumps(payload).encode()
        with patch('sys.stdin', io.TextIOWrapper(io.BytesIO(raw))):
            self.assertEqual(agent_observe.main(list(args)), 0)

    def lines(self, k=0):
        path = self.gen / f'events.{k}.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []


class WriterTests(WriterBase):
    def test_keeps_only_the_contract_fields(self):
        self.run_hook({'hook_event_name': 'SessionStart', 'session_id': SID, 'source': 'resume',
                       'model': 'claude-fable-5-1', 'transcript_path': '/home/x', 'cwd': '/workdir'})
        self.run_hook({'hook_event_name': 'PostModelSwitch', 'session_id': SID, 'to_model': 'claude-opus-5-5'})
        self.run_hook({'hook_event_name': 'Stop', 'effort': {'level': 'high'}, 'last_assistant_message': 'secret'})
        self.run_hook({'hook_event_name': 'Stop', 'agent_id': 'a1', 'effort': {'level': 'low'}})
        allowed = {'v', 'event', 'session_id', 'source', 'model', 'effort', 'subagent', 'state', 'pid', 'ts', 'dur_ms'}
        self.assertTrue(all(set(line) <= allowed for line in self.lines()), 'message text is never kept')
        start, switch, stop, sub = self.lines()
        self.assertEqual(set(start) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'source', 'model'})
        self.assertEqual((start['source'], start['model']), ('resume', 'claude-fable-5-1'))
        self.assertEqual(switch['model'], 'claude-opus-5-5')
        self.assertEqual(stop['effort'], 'high')
        self.assertEqual(stop['state'], 'idle')
        self.assertNotIn('subagent', stop)
        self.assertTrue(sub['subagent'])
        self.assertNotIn('state', sub)
        self.assertTrue(start['ts'].endswith('Z'))

    def test_observed_payload_shapes(self):
        # Shapes seen with Claude Code 2.1.308 in the isolated P3 run: model
        # only on a startup SessionStart, effort.level on main Stops, agent
        # fields only on SubagentStop, whose session_id can name a newer
        # conversation, and extra keys on a resumed start.
        other = '7c9e6679-7425-40de-944b-e07fc1f90ae7'
        self.run_hook({'hook_event_name': 'SessionStart', 'session_id': SID, 'source': 'startup',
                       'model': 'claude-sonnet-5-5', 'transcript_path': '/t', 'cwd': '/w', 'permission_mode': 'default'})
        self.run_hook({'hook_event_name': 'Stop', 'session_id': SID, 'effort': {'level': 'low'},
                       'stop_hook_active': False, 'background_tasks': 1})
        self.run_hook({'hook_event_name': 'SessionEnd', 'session_id': SID, 'reason': 'clear'})
        self.run_hook({'hook_event_name': 'SessionStart', 'session_id': other, 'source': 'clear'})
        self.run_hook({'hook_event_name': 'SubagentStop', 'session_id': other, 'agent_id': 'a1',
                       'agent_type': 'general-purpose'})
        self.run_hook({'hook_event_name': 'SessionStart', 'session_id': SID, 'source': 'resume',
                       'context_tokens': 1234, 'estimated_cache_write_usd': 0.1,
                       'prompt_cache_likely_expired': True, 'seconds_since_last_response': 60})
        start, stop, end, clear, sub, resume = self.lines()
        self.assertEqual(start['model'], 'claude-sonnet-5-5')
        self.assertEqual(stop['effort'], 'low')
        self.assertNotIn('subagent', stop)
        self.assertNotIn('reason', end)
        self.assertNotIn('model', clear, 'a missing model is never invented')
        self.assertTrue(sub['subagent'])
        self.assertEqual(sub['session_id'], other)
        self.assertEqual(set(resume) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'source'})

    def test_writes_nothing_without_generation_directory_or_event(self):
        for env in ({}, {'YOLOCTL_AGENT_GEN': '0'}, {'YOLOCTL_AGENT_GEN': '03'}, {'YOLOCTL_AGENT_GEN': 'x'}):
            with patch.dict(os.environ, env, clear=True):
                self.run_hook({'hook_event_name': 'Stop'})
        with patch.dict(os.environ, {'YOLOCTL_AGENT_GEN': '4'}):
            self.run_hook({'hook_event_name': 'Stop'})
        self.run_hook({'hook_event_name': 'PreToolUse'})
        self.run_hook(b'not json')
        self.assertEqual(sorted(p.name for p in self.root.iterdir()), ['gen-3'], 'never creates directories')
        self.assertEqual(self.lines(), [])
        self.run_hook({'hook_event_name': 'Stop', 'model': 'm' * 5000})
        self.assertNotIn('model', self.lines()[0], 'an oversized field is dropped, not truncated')
        self.run_hook({'hook_event_name': 'Stop', 'model': 'caf\u00e9', 'effort': {'level': 'auto'}})
        self.assertEqual(set(self.lines()[1]) & {'model', 'effort'}, set(),
                         'a value the reader would refuse is left out, never invalidating the line')

    def test_never_follows_links(self):
        outside = self.root.parent / 'outside'
        outside.write_text('')
        (self.gen / 'events.0.jsonl').symlink_to(outside)
        self.run_hook({'hook_event_name': 'Stop'})
        self.assertEqual(outside.read_text(), '')
        (self.gen / 'events.0.jsonl').unlink()
        (self.root / 'gen-5').symlink_to(self.gen)
        with patch.dict(os.environ, {'YOLOCTL_AGENT_GEN': '5'}):
            self.run_hook({'hook_event_name': 'Stop'})
        self.assertEqual(sorted(p.name for p in self.gen.iterdir()), ['events.lock'])

    def test_drops_the_event_on_lock_contention(self):
        self.run_hook({'hook_event_name': 'Stop'})
        fd = os.open(self.gen / 'events.lock', os.O_WRONLY)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX)
            started = time.monotonic()
            self.run_hook({'hook_event_name': 'SessionEnd'})
            self.assertLess(time.monotonic() - started, 2)
        finally:
            os.close(fd)
        self.assertEqual([e['event'] for e in self.lines()], ['Stop'])

    def test_rotates_at_the_file_cap_and_keeps_two_files(self):
        with patch.object(agent_observe, 'FILE_BYTES', 300):
            for _ in range(12):
                self.run_hook({'hook_event_name': 'Stop', 'effort': {'level': 'high'}})
        names = sorted(p.name for p in self.gen.iterdir() if p.name.startswith('events.') and p.suffix == '.jsonl')
        self.assertEqual(len(names), 2)
        for name in names:
            self.assertLessEqual((self.gen / name).stat().st_size, 300)
        k = max(int(n.split('.')[1]) for n in names)
        self.assertEqual(names, sorted([f'events.{k - 1}.jsonl', f'events.{k}.jsonl']))

    def test_a_full_tmpfs_leaves_no_partial_line(self):
        self.run_hook({'hook_event_name': 'Stop'})
        path = self.gen / 'events.0.jsonl'
        before = path.read_bytes()
        real_write = os.write

        def full(fd, data):
            raise OSError(errno.ENOSPC, 'No space left on device')

        def short(fd, data):
            return real_write(fd, data[:7])

        for write in (full, short):
            with patch.object(agent_observe.os, 'write', side_effect=write):
                self.run_hook({'hook_event_name': 'SessionEnd'})
            self.assertEqual(path.read_bytes(), before)

    def test_claude_2_1_311_vectors(self):
        for name in ('SessionStart-startup', 'Stop', 'SessionEnd', 'SessionStart-clear', 'SessionStart-resume'):
            self.run_hook((VECTORS / f'{name}.json').read_bytes())
        self.run_hook((VECTORS / 'statusline.json').read_bytes(), ['statusline'])
        got = []
        for line in self.lines():
            self.assertGreaterEqual(line.pop('dur_ms'), 0)
            line.update(pid=42, ts='2026-10-27T00:00:00Z')
            got.append(line)
        want = [json.loads(line) for line in (VECTORS / 'expected.jsonl').read_text().splitlines()]
        self.assertEqual(got, want)

    def test_claude_state_vectors(self):
        for case in json.loads((CLAUDE_STATE_VECTORS / 'payloads.json').read_text()):
            self.run_hook(case['payload'])
        got = []
        for line in self.lines():
            self.assertGreaterEqual(line.pop('dur_ms'), 0)
            line.update(pid=42, ts='2026-10-28T00:00:00Z')
            got.append(line)
        want = [json.loads(line) for line in (CLAUDE_STATE_VECTORS / 'expected.jsonl').read_text().splitlines()]
        self.assertEqual(got, want)

    def test_claude_never_records_a_model_request(self):
        # The fold reads a completed model request as recovery from a failed
        # one; only the Codex writer may produce that line.
        self.assertNotIn('PostModelRequest', agent_observe.EVENTS)
        self.run_hook({'hook_event_name': 'PostModelRequest', 'session_id': SID, 'status': 'completed',
                       'token_usage': {'total_tokens': 5}})
        self.assertEqual(self.lines(), [])

    def test_claude_state_hooks_drop_private_fields(self):
        private = {'prompt': 'private prompt', 'message': 'private notification',
                   'last_assistant_message': 'private assistant text', 'tool_input': {'command': 'private tool'},
                   'error': 'private error', 'error_details': 'private details',
                   'transcript_path': '/private/transcript', 'cwd': '/private/workdir',
                   'background_tasks': [{'command': 'private command', 'description': 'private task'}],
                   'session_crons': [{'prompt': 'private cron'}]}
        for event in ('UserPromptSubmit', 'Stop', 'StopFailure', 'Notification'):
            self.run_hook(dict(private, hook_event_name=event, session_id=SID,
                               notification_type='permission_prompt'))
        for line in self.lines():
            self.assertEqual(set(line), {'v', 'event', 'session_id', 'pid', 'ts', 'dur_ms', 'state'})
            self.assertNotIn('private', json.dumps(line))
        self.assertEqual([line['state'] for line in self.lines()],
                         ['working', 'working', 'failed', 'waiting_permission'])
        # Notification remains state-only even if extra value fields arrive.
        self.run_hook({'hook_event_name': 'Notification', 'notification_type': 'idle_prompt',
                       'model': 'm', 'effort': {'level': 'high'}, 'source': 'startup',
                       'context_window': {'context_window_size': 200000, 'used_percentage': 10}})
        self.assertEqual(set(self.lines()[-1]), {'v', 'event', 'pid', 'ts', 'dur_ms', 'state'})

    def test_unrecognized_notifications_write_nothing(self):
        for kind in (None, '', 'auth_success', 'elicitation_complete', 'agent_completed', 'unknown', [], {}):
            self.run_hook({'hook_event_name': 'Notification', 'notification_type': kind})
        self.run_hook({'hook_event_name': 'Notification'})
        self.assertEqual(self.lines(), [])
        self.assertEqual(list(self.gen.iterdir()), [], 'no files for an ignored notification')

    def test_subagent_hooks_never_report_box_state(self):
        for marker in ('agent_id', 'agent_type'):
            for event in ('UserPromptSubmit', 'Stop', 'StopFailure', 'SubagentStop'):
                self.run_hook({'hook_event_name': event, marker: 'child'})
            for kind in agent_observe.NOTIFICATION_STATES:
                self.run_hook({'hook_event_name': 'Notification', 'notification_type': kind, marker: 'child'})
        self.assertEqual(len(self.lines()), 8, 'subagent notifications are omitted')
        for line in self.lines():
            self.assertTrue(line['subagent'])
            self.assertNotIn('state', line)

    def test_stop_state_uses_only_nonempty_task_or_cron_arrays(self):
        for tasks, crons, want in ((None, None, 'idle'), ([], [], 'idle'),
                                   ([{}], [], 'working'), ([], [{}], 'working'),
                                   ([{}], [{}], 'working'), (1, 'pending', 'idle'),
                                   ({'status': 'running'}, True, 'idle')):
            with self.subTest(tasks=tasks, crons=crons):
                self.run_hook({'hook_event_name': 'Stop', 'background_tasks': tasks, 'session_crons': crons})
                self.assertEqual(self.lines()[-1]['state'], want)

    def test_statusline_keeps_only_the_contract_fields(self):
        payload = json.loads((VECTORS / 'statusline.json').read_bytes())
        self.run_hook(payload, ['statusline'])
        line = self.lines()[0]
        self.assertEqual(set(line), {'v', 'event', 'pid', 'ts', 'dur_ms', 'wrapped', 'session_id', 'model',
                                     'effort', 'version', 'ctx_size', 'ctx_pct', 'ctx_used'})
        self.assertEqual((line['event'], line['model'], line['effort'], line['version']),
                         ('statusline', 'claude-opus-5-5', 'high', '2.1.311'))
        # The documented basis of used_percentage: input + cache creation + cache read.
        self.assertEqual((line['ctx_size'], line['ctx_pct'], line['ctx_used']), (1000000, 4, 4 + 16 + 39743))
        self.assertIs(line['wrapped'], False)
        for window, want in (({'context_window_size': 200000, 'used_percentage': 42, 'current_usage': None},
                              {'ctx_size': 200000, 'ctx_pct': 42}),
                             ({'context_window_size': 200000, 'used_percentage': 42.5}, {}),
                             ({'used_percentage': 42}, {}),
                             ({'context_window_size': True, 'used_percentage': 42}, {}),
                             ({'context_window_size': 200000, 'used_percentage': 101}, {}),
                             ({'context_window_size': 200000, 'used_percentage': 0,
                               'current_usage': {'input_tokens': 1, 'cache_read_input_tokens': 2}},
                              {'ctx_size': 200000, 'ctx_pct': 0})):
            self.run_hook({'model': {'id': 'x'}, 'context_window': window}, ['statusline'])
            last = self.lines()[-1]
            self.assertEqual({k: v for k, v in last.items() if k.startswith('ctx_')}, want, window)
        self.run_hook({'model': 'claude-opus-5-5', 'effort': 'high', 'version': 'v\n1'}, ['statusline'])
        self.assertEqual(set(self.lines()[-1]) & {'model', 'effort', 'version'}, {'model', 'effort'},
                         'a control character drops the version')

    def test_stage_prefers_an_unnamed_tmp_file_then_a_pipe(self):
        # The real /tmp: memfd_create and /dev/shm are refused in the box.
        raw = b'{"model":{"id":"x"}}'
        fd, why = agent_observe.stage(raw)
        self.assertIsNone(why)
        try:
            st = os.fstat(fd)
            self.assertTrue(stat.S_ISREG(st.st_mode))
            self.assertEqual(st.st_nlink, 0, 'O_TMPFILE: no name anyone else can open')
            self.assertEqual(os.read(fd, 100), raw, 'rewound to the start')
        finally:
            os.close(fd)
        with patch.object(agent_observe, 'TMP', str(self.root / 'missing')):
            fd, why = agent_observe.stage(raw)
            self.assertIsNone(why)
            try:
                self.assertTrue(stat.S_ISFIFO(os.fstat(fd).st_mode))
                self.assertEqual(os.read(fd, 100), raw)
            finally:
                os.close(fd)
            self.assertEqual(agent_observe.stage(b' ' * agent_observe.PIPE_BYTES), (None, 'too-large'))

    def test_user_command_reads_only_a_user_scope_command(self):
        home = self.root.parent / 'home'
        config = home / '.claude'
        config.mkdir(parents=True)
        settings = config / 'settings.json'
        for content, want in ((json.dumps({'statusLine': {'type': 'command', 'command': 'echo hi'}}), 'echo hi'),
                              (json.dumps({'statusLine': {'type': 'static', 'command': 'echo hi'}}), None),
                              (json.dumps({'statusLine': {'type': 'command', 'command': ' '}}), None),
                              (json.dumps({'statusLine': 'echo hi'}), None),
                              ('{"statusLine":', None),
                              ('[]', None)):
            settings.write_text(content)
            with patch.dict(os.environ, {'HOME': str(home)}, clear=True):
                self.assertEqual(agent_observe.user_command(), want, content)
        other = self.root.parent / 'config'
        other.mkdir()
        (other / 'settings.json').write_text(json.dumps({'statusLine': {'type': 'command', 'command': 'theirs'}}))
        with patch.dict(os.environ, {'HOME': str(home), 'CLAUDE_CONFIG_DIR': str(other)}, clear=True):
            self.assertEqual(agent_observe.user_command(), 'theirs', 'CLAUDE_CONFIG_DIR first')
        with patch.dict(os.environ, {'HOME': str(home), 'CLAUDE_CONFIG_DIR': 'relative'}, clear=True):
            self.assertIsNone(agent_observe.user_command(), 'a relative directory is not trusted to mean home')
        settings.unlink()
        os.mkfifo(settings)
        with patch.dict(os.environ, {'HOME': str(home)}, clear=True):
            started = time.monotonic()
            self.assertIsNone(agent_observe.user_command(), 'a FIFO is not a settings file')
            self.assertLess(time.monotonic() - started, 2)


class StatusLineHandOverTests(unittest.TestCase):
    """The wrapper as Claude runs it: a process that records, then execs."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.dir = Path(temporary.name)
        self.root = self.dir / 'agent'
        self.gen = self.root / 'gen-3'
        self.gen.mkdir(parents=True, mode=0o700)
        self.config = self.dir / 'config'
        self.config.mkdir()
        self.payload = (VECTORS / 'statusline.json').read_bytes()

    def run_wrapper(self, command=None, env=None, payload=None, tmp=None):
        if command is not None:
            (self.config / 'settings.json').write_text(json.dumps({'statusLine': {'type': 'command', 'command': command}}))
        driver = ('import importlib.util, sys\n'
                  'spec = importlib.util.spec_from_file_location("w", sys.argv[1])\n'
                  'w = importlib.util.module_from_spec(spec); spec.loader.exec_module(w)\n'
                  'w.ROOT = sys.argv[2]\n'
                  'w.TMP = sys.argv[3]\n'
                  'sys.exit(w.main(["statusline"]))\n')
        environment = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(self.dir),
                       'CLAUDE_CONFIG_DIR': str(self.config), 'YOLOCTL_AGENT_GEN': '3'}
        environment.update(env or {})
        return subprocess.run([sys.executable, '-B', '-I', '-S', '-c', driver, str(WRITER), str(self.root), tmp or '/tmp'],
                              input=self.payload if payload is None else payload, capture_output=True,
                              env={k: v for k, v in environment.items() if v is not None}, timeout=30)

    def lines(self):
        path = self.gen / 'events.0.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_records_then_runs_the_users_command_with_the_same_input(self):
        events = self.gen / 'events.0.jsonl'
        # The user's command sees the record already written, the exact
        # bytes Claude sent, and decides the exit status.
        done = self.run_wrapper(f'grep -c statusline {events} >&2; cat; printf " %s" "$YOLOCTL_STATUSLINE_WRAPPED"; exit 3')
        self.assertEqual(done.returncode, 3)
        self.assertEqual(done.stdout, self.payload + b' 1')
        self.assertEqual(done.stderr.strip(), b'1', 'recorded before the hand-over')
        line, = self.lines()
        self.assertIs(line['wrapped'], True)
        self.assertNotIn('handover', line)

    def test_without_a_user_command_prints_nothing(self):
        done = self.run_wrapper()
        self.assertEqual((done.returncode, done.stdout, done.stderr), (0, b'', b''))
        line, = self.lines()
        self.assertIs(line['wrapped'], False)

    def test_a_recording_failure_still_runs_the_users_command(self):
        done = self.run_wrapper('cat', env={'YOLOCTL_AGENT_GEN': None})
        self.assertEqual((done.returncode, done.stdout), (0, self.payload))
        done = self.run_wrapper('cat', payload=b'not json')
        self.assertEqual((done.returncode, done.stdout), (0, b'not json'))
        self.assertEqual(self.lines(), [])

    def test_an_unstaged_payload_skips_the_hand_over_and_says_why(self):
        big = json.dumps({'model': {'id': 'claude-opus-5-5'}, 'pad': ' ' * 70000}).encode()
        done = self.run_wrapper('echo ran', payload=big, tmp=str(self.dir / 'missing'))
        self.assertEqual((done.returncode, done.stdout), (0, b''))
        line, = self.lines()
        self.assertEqual((line['wrapped'], line['handover'], line['model']), (False, 'too-large', 'claude-opus-5-5'))

    def test_a_user_command_naming_the_wrapper_does_not_recurse(self):
        # The user's command runs with the marker set, so the wrapper run
        # again from it neither records nor hands over a second time.
        done = self.run_wrapper('cat', env={'YOLOCTL_STATUSLINE_WRAPPED': '1'})
        self.assertEqual((done.returncode, done.stdout), (0, b''))
        self.assertEqual(self.lines(), [])


AGY_VECTORS = REPO / 'internal/agentsession/observe/testdata/agy-docs-synthetic'


class AgyWriterTests(WriterBase):
    """agy mode: the documented status line fields, never the rest."""

    def payload(self, **changes):
        payload = json.loads((AGY_VECTORS / 'idle.json').read_bytes())
        payload.update(changes)
        return payload

    def test_agy_docs_synthetic_vectors(self):
        for name in (AGY_VECTORS / 'ORDER').read_text().split():
            self.run_hook((AGY_VECTORS / f'{name}.json').read_bytes(), ['agy'])
        got = []
        for line in self.lines():
            self.assertGreaterEqual(line.pop('dur_ms'), 0)
            line.update(pid=42, ts='2026-10-28T00:00:00Z')
            got.append(line)
        want = [json.loads(line) for line in (AGY_VECTORS / 'expected.jsonl').read_text().splitlines()]
        self.assertEqual(got, want)

    def test_keeps_only_the_agy_contract_fields(self):
        self.run_hook(self.payload(pending_input_count=3), ['agy'])
        line = self.lines()[0]
        self.assertEqual(set(line), {'v', 'event', 'pid', 'ts', 'dur_ms', 'wrapped', 'session_id', 'model',
                                     'effort', 'version', 'ctx_size', 'ctx_pct', 'ctx_used', 'state'})
        raw = (self.gen / 'events.0.jsonl').read_text()
        for leak in ('someone@example.com', 'Fix the build', 'transcript', '/workdir', 'Pro', 'explorer', '0.4213'):
            self.assertNotIn(leak, raw)
        self.assertEqual(line['state'], 'idle', 'queued input is not a wait')

    def test_maps_agent_state_and_confirmation(self):
        for changes, want in (({'agent_state': 'working'}, 'working'),
                              ({'agent_state': 'tool_use'}, 'working'),
                              ({'agent_state': 'reviewing'}, 'working'),
                              ({'agent_state': 'idle'}, 'idle'),
                              ({'agent_state': 'error'}, 'failed'),
                              ({'agent_state': 'idle', 'tool_confirmation_pending': True}, 'waiting_permission'),
                              ({'agent_state': 'initializing'}, None),
                              ({'agent_state': 'authenticating'}, None),
                              ({'agent_state': 'thinking'}, None),
                              ({'agent_state': 'WORKING'}, None),
                              ({'agent_state': ['working']}, None),
                              ({'agent_state': 'idle', 'tool_confirmation_pending': 'true'}, 'idle')):
            self.run_hook(self.payload(**changes), ['agy'])
            self.assertEqual(self.lines()[-1].get('state'), want, changes)
        self.assertTrue(all(line.get('state', 'idle') in agent_observe.STATES for line in self.lines()),
                        'an unmapped agent value writes no state field')

    def test_agy_values_and_bounds(self):
        window = {'context_window_size': 1048576, 'used_percentage': 14.24,
                  'current_usage': {'input_tokens': 1, 'cache_creation_input_tokens': 2, 'cache_read_input_tokens': 3}}
        for changes, want in (({}, {'ctx_size': 1048576, 'ctx_pct': 14, 'ctx_used': 6}),
                              ({'used_percentage': 99.5}, {'ctx_size': 1048576, 'ctx_pct': 100, 'ctx_used': 6}),
                              ({'used_percentage': 100.01}, {}),
                              ({'used_percentage': True}, {}),
                              ({'used_percentage': None}, {}),
                              ({'context_window_size': 0}, {'ctx_used': 6}),
                              ({'context_window_size': 0, 'current_usage': None}, {}),
                              ({'current_usage': {'input_tokens': 1}}, {'ctx_size': 1048576, 'ctx_pct': 14}),
                              ({'context_window_size': -1}, {})):
            self.run_hook(self.payload(context_window=dict(window, **changes)), ['agy'])
            last = self.lines()[-1]
            self.assertEqual({k: v for k, v in last.items() if k.startswith('ctx_')}, want, changes)
        for changes, want in (({'model': None}, {}),
                              ({'model': {'id': 'gemini-3.8-flash-low'}}, {'model': 'gemini-3.8-flash-low'}),
                              ({'model': {'id': 'x', 'effort': 'turbo'}}, {'model': 'x'}),
                              ({'model': 'gemini'}, {}),
                              ({'conversation_id': ''}, None),
                              ({'conversation_id': 'BAD9968C-952F-48BD-8C7B-035E4F0B8858'}, 'bad9968c-952f-48bd-8c7b-035e4f0b8858'),
                              ({'conversation_id': 'not-a-uuid'}, None)):
            self.run_hook(self.payload(**changes), ['agy'])
            last = self.lines()[-1]
            if 'conversation_id' in changes:
                self.assertEqual(last.get('session_id'), want, changes)
            else:
                self.assertEqual({k: last[k] for k in ('model', 'effort') if k in last}, want, changes)

    def test_chained_command_is_the_saved_previous_one(self):
        home = self.root.parent / 'home'
        saved = home / '.gemini/antigravity-cli' / 'yoloctl-statusline.json'
        saved.parent.mkdir(parents=True)
        for content, want in ((json.dumps({'v': 1, 'previous': {'type': 'command', 'command': 'echo hi'}}), 'echo hi'),
                              (json.dumps({'v': 1, 'previous': {'command': 'echo hi', 'padding': 1}}), 'echo hi'),
                              (json.dumps({'v': 1, 'previous': {'command': 'echo hi', 'enabled': False}}), None),
                              (json.dumps({'v': 1, 'previous': {'type': 'static', 'command': 'echo hi'}}), None),
                              (json.dumps({'v': 1, 'previous': None}), None),
                              (json.dumps({'v': 2, 'previous': {'command': 'echo hi'}}), None),
                              (json.dumps({'previous': {'command': 'echo hi'}}), None),
                              ('{', None)):
            saved.write_text(content)
            with patch.dict(os.environ, {'HOME': str(home)}, clear=True):
                self.assertEqual(agent_observe.agy_command(), want, content)
        with patch.dict(os.environ, {'HOME': 'relative'}, clear=True):
            self.assertIsNone(agent_observe.agy_command())
        saved.unlink()
        os.mkfifo(saved)
        with patch.dict(os.environ, {'HOME': str(home)}, clear=True):
            self.assertIsNone(agent_observe.agy_command(), 'a FIFO is not a saved command')


class AgyHandOverTests(unittest.TestCase):
    """The agy wrapper as agy runs it: record, then exec the saved command."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.dir = Path(temporary.name)
        self.root = self.dir / 'agent'
        self.gen = self.root / 'gen-3'
        self.gen.mkdir(parents=True, mode=0o700)
        self.settings = self.dir / '.gemini/antigravity-cli'
        self.settings.mkdir(parents=True)
        self.payload = (AGY_VECTORS / 'working.json').read_bytes()

    def run_wrapper(self, previous):
        (self.settings / 'yoloctl-statusline.json').write_text(json.dumps({'v': 1, 'previous': previous}))
        driver = ('import importlib.util, sys\n'
                  'spec = importlib.util.spec_from_file_location("w", sys.argv[1])\n'
                  'w = importlib.util.module_from_spec(spec); spec.loader.exec_module(w)\n'
                  'w.ROOT = sys.argv[2]\n'
                  'sys.exit(w.main(["agy"]))\n')
        environment = {'PATH': os.environ.get('PATH', '/usr/bin:/bin'), 'HOME': str(self.dir), 'YOLOCTL_AGENT_GEN': '3'}
        return subprocess.run([sys.executable, '-B', '-I', '-S', '-c', driver, str(WRITER), str(self.root)],
                              input=self.payload, capture_output=True, env=environment, timeout=30)

    def lines(self):
        path = self.gen / 'events.0.jsonl'
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def test_records_then_runs_the_saved_command(self):
        done = self.run_wrapper({'type': 'command', 'command': 'cat; exit 4', 'padding': 1})
        self.assertEqual((done.returncode, done.stdout), (4, self.payload))
        line, = self.lines()
        self.assertEqual((line['wrapped'], line['state']), (True, 'working'))

    def test_without_a_saved_command_prints_nothing(self):
        done = self.run_wrapper(None)
        self.assertEqual((done.returncode, done.stdout, done.stderr), (0, b'', b''))
        line, = self.lines()
        self.assertIs(line['wrapped'], False)


class FakeCodexDaemon:
    """A Unix-socket app-server answering initialize and one thread/read over
    a WebSocket upgrade of /rpc, as Codex 0.164/0.165 does."""

    def __init__(self, path, thread=None, delay=0, reset=False):
        import base64
        import hashlib
        import socket
        import threading
        self.b64, self.sha1, self.thread, self.delay, self.reset = base64, hashlib.sha1, thread, delay, reset
        self.reads = []
        self.server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.server.bind(path)
        self.server.listen(4)
        self.worker = threading.Thread(target=self.serve, daemon=True)
        self.worker.start()

    def close(self):
        self.server.close()

    def serve(self):
        try:
            conn, _ = self.server.accept()
        except OSError:
            return
        with conn:
            if self.reset:
                return
            try:
                self.answer(conn)
            except (OSError, ValueError):
                pass

    def answer(self, conn):
        buf = b''
        while b'\r\n\r\n' not in buf:
            buf += conn.recv(4096)
        head, buf = buf.split(b'\r\n\r\n', 1)
        key = [line.split(b':', 1)[1].strip() for line in head.split(b'\r\n') if line.lower().startswith(b'sec-websocket-key:')][0]
        accept = self.b64.b64encode(self.sha1(key + agent_observe.WS_GUID.encode()).digest())
        conn.sendall(b'HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n'
                     b'Sec-WebSocket-Accept: ' + accept + b'\r\n\r\n')
        while True:
            while len(buf) < 2:
                chunk = conn.recv(4096)
                if not chunk:
                    return
                buf += chunk
            n, offset = buf[1] & 0x7f, 2
            if n == 126:
                n, offset = int.from_bytes(buf[2:4], 'big'), 4
            while len(buf) < offset + 4 + n:
                buf += conn.recv(4096)
            mask, payload = buf[offset:offset + 4], buf[offset + 4:offset + 4 + n]
            opcode, buf = buf[0] & 0x0f, buf[offset + 4 + n:]
            if opcode == 0x8:
                return
            msg = json.loads(bytes(b ^ mask[i % 4] for i, b in enumerate(payload)))
            if 'id' not in msg:
                continue
            if msg['method'] == 'thread/read':
                self.reads.append(msg['params'])
                time.sleep(self.delay)
                # A notification first: the reader must skip it.
                self.frame(conn, {'method': 'thread/status/changed', 'params': {}})
                result = {'thread': self.thread}
            else:
                result = {'userAgent': 'codex/0.165.0'}
            self.frame(conn, {'id': msg['id'], 'result': result})

    @staticmethod
    def frame(conn, value):
        data = json.dumps(value).encode()
        header = bytes([0x81, len(data)]) if len(data) < 126 else bytes([0x81, 126]) + len(data).to_bytes(2, 'big')
        conn.sendall(header + data)


class CodexWriterTests(WriterBase):
    """Codex hook mode: identity and model from the hook, then one bounded
    thread/read of the box's own daemon for model and effort."""

    def setUp(self):
        super().setUp()
        self.daemons = self.root.parent / 'codex-daemon-%d'
        self.dir = Path(str(self.daemons) % os.getuid())
        self.dir.mkdir(mode=0o700)
        self.peer = (b'/home/sandbox/.codex/packages/app-server-daemon/releases/0.165.0/codex', b'app-server', b'--listen')
        real = agent_observe.peer_identity
        self.identity = lambda pid: (real(pid) or (0,))[:1] + (self.peer,) if pid == os.getpid() else real(pid)
        for patcher in (patch.object(agent_observe, 'DAEMON_DIR', str(self.daemons)),
                        patch.object(agent_observe, 'peer_identity', lambda pid: self.identity(pid))):
            patcher.start()
            self.addCleanup(patcher.stop)

    def daemon(self, name='235323ca', **kwargs):
        thread = kwargs.pop('thread', {'id': SID, 'model': 'gpt-5.6-luna', 'reasoningEffort': 'high',
                                       'status': {'type': 'idle'}, 'cliVersion': '0.165.0', 'preview': 'secret'})
        d = FakeCodexDaemon(str(self.dir / name), thread=thread, **kwargs)
        self.addCleanup(d.close)
        return d

    def start(self, **extra):
        payload = {'hook_event_name': 'SessionStart', 'session_id': SID.upper(), 'source': 'startup',
                   'model': 'gpt-5.6-luna', 'transcript_path': '/t', 'cwd': '/w', 'permission_mode': 'default',
                   'turn_id': 'x'}
        payload.update(extra)
        self.run_hook(payload, ['codex'])

    def test_codex_0_164_synthetic_vectors(self):
        for name in ('SessionStart-startup', 'Stop', 'SubagentStart', 'SessionStart-resume', 'SessionEnd',
                     'UserPromptSubmit', 'PostModelRequest', 'PermissionRequest'):
            self.run_hook((CODEX_VECTORS / f'{name}.json').read_bytes(), ['codex'])
        got = []
        for line in self.lines():
            self.assertGreaterEqual(line.pop('dur_ms'), 0)
            line.update(pid=42, ts='2026-10-28T00:00:00Z')
            got.append(line)
        want = [json.loads(line) for line in (CODEX_VECTORS / 'expected.jsonl').read_text().splitlines()]
        self.assertEqual(got, want)

    def test_keeps_only_the_codex_contract_fields(self):
        self.start()
        self.run_hook({'hook_event_name': 'SubagentStart', 'session_id': SID, 'model': 'gpt-5.6-luna'}, ['codex'])
        self.run_hook({'hook_event_name': 'SessionStart', 'session_id': 'not-a-uuid', 'source': 'bogus'}, ['codex'])
        self.run_hook({'hook_event_name': 'PreToolUse', 'session_id': SID}, ['codex'])
        start, sub, odd = self.lines()
        self.assertEqual(set(start) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'source', 'model'})
        self.assertEqual((start['session_id'], start['source']), (SID, 'startup'), 'canonical lowercase UUID')
        self.assertTrue(sub['subagent'])
        self.assertNotIn('session_id', odd)
        self.assertNotIn('source', odd)

    def test_a_stale_socket_of_a_stopped_daemon_is_skipped(self):
        # A daemon the holder killed can leave its socket file behind; it
        # sorts first here and refuses connections.
        import socket
        stale = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        stale.bind(str(self.dir / '00000000'))
        stale.close()
        d = self.daemon()
        self.start()
        hook, query = self.lines()
        self.assertEqual((query['event'], query['model']), ('query', 'gpt-5.6-luna'))
        self.assertEqual(len(d.reads), 1)

    def test_main_start_and_stop_add_the_daemons_current_values(self):
        d = self.daemon()
        self.start()
        hook, query = self.lines()
        self.assertEqual(hook['event'], 'SessionStart')
        self.assertEqual(set(query) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'model', 'effort', 'state'})
        self.assertEqual((query['event'], query['session_id'], query['model'], query['effort'], query['state']),
                         ('query', SID, 'gpt-5.6-luna', 'high', 'idle'), 'labelled with the thread it read')
        self.assertEqual(d.reads, [{'threadId': SID, 'includeTurns': False}])
        self.assertLess(query['dur_ms'], 1000)

    def test_the_daemon_status_gives_the_state(self):
        for status, want in (({'type': 'idle'}, 'idle'),
                             ({'type': 'active', 'activeFlags': []}, 'working'),
                             ({'type': 'active', 'activeFlags': ['waitingOnApproval']}, 'waiting_permission'),
                             ({'type': 'active', 'activeFlags': ['waitingOnUserInput']}, 'waiting_input')):
            with self.subTest(status):
                for f in list(self.gen.glob('events.*')) + list(self.dir.iterdir()):
                    f.unlink()
                self.daemon(thread={'id': SID, 'model': 'gpt-5.6-luna', 'reasoningEffort': 'high', 'status': status})
                self.start()
                self.assertEqual(self.lines()[-1]['state'], want)

    def test_a_stop_query_leaves_the_state_to_the_stop_hook(self):
        # The daemon may still call a finishing turn active.
        self.daemon(thread={'id': SID, 'model': 'gpt-5.6-luna', 'reasoningEffort': 'high',
                            'status': {'type': 'active', 'activeFlags': []}})
        self.run_hook({'hook_event_name': 'Stop', 'session_id': SID, 'model': 'gpt-5.6-luna', 'turn_id': 't',
                       'last_assistant_message': 'secret'}, ['codex'])
        stop, query = self.lines()
        self.assertEqual((stop['state'], query['event']), ('idle', 'query'))
        self.assertNotIn('state', query)
        self.assertNotIn('last_assistant_message', stop)

    def test_turn_permission_and_model_request_states(self):
        self.run_hook({'hook_event_name': 'UserPromptSubmit', 'session_id': SID, 'turn_id': 't', 'model': 'gpt-5.6-luna',
                       'prompt': 'secret', 'cwd': '/w'}, ['codex'])
        self.run_hook({'hook_event_name': 'PermissionRequest', 'session_id': SID, 'turn_id': 't', 'model': 'gpt-5.6-luna',
                       'tool_name': 'shell', 'tool_input': {'command': 'secret'}}, ['codex'])
        self.run_hook({'hook_event_name': 'PostModelRequest', 'session_id': SID, 'turn_id': 't', 'model': 'gpt-5.6-mini',
                       'status': 'completed', 'error': None,
                       'token_usage': {'input_tokens': 170000, 'cached_input_tokens': 9000, 'output_tokens': 2000,
                                       'reasoning_output_tokens': 500, 'total_tokens': 172000}}, ['codex'])
        self.run_hook({'hook_event_name': 'PostModelRequest', 'session_id': SID, 'turn_id': 't', 'model': 'gpt-5.6-luna',
                       'status': 'failed', 'error': 'secret upstream text', 'token_usage': None}, ['codex'])
        prompt, permission, used, failed = self.lines()
        self.assertEqual((prompt['state'], prompt['model']), ('working', 'gpt-5.6-luna'))
        self.assertEqual(set(permission) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'state'},
                         'a permission line carries only the state')
        self.assertEqual(permission['state'], 'waiting_permission')
        self.assertEqual(set(used) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'session_id', 'ctx_used', 'state'},
                         'a model request reports the tokens used and its outcome, never its model or error')
        self.assertEqual((used['ctx_used'], used['state']), (172000, 'working'))
        self.assertEqual((failed['state'], 'ctx_used' in failed), ('failed', False))
        raw = (self.gen / 'events.0.jsonl').read_text()
        self.assertNotIn('secret', raw, 'prompts, tool input and error text are never kept')

    def test_only_a_completed_model_request_reports_working(self):
        request = {'hook_event_name': 'PostModelRequest', 'session_id': SID, 'turn_id': 't', 'model': 'gpt-5.6-luna'}
        cases = [({'status': 'completed', 'token_usage': None}, 'working'),
                 ({'status': 'completed'}, 'working'),
                 ({'status': 'cancelled', 'token_usage': {'total_tokens': 5}}, None),
                 ({'token_usage': {'total_tokens': 5}}, None),
                 ({'status': 'retrying', 'token_usage': {'total_tokens': 5}}, None),
                 ({'status': 'COMPLETED', 'token_usage': {'total_tokens': 5}}, None)]
        for extra, _ in cases:
            self.run_hook({**request, **extra}, ['codex'])
        got = self.lines()
        self.assertEqual([line.get('state') for line in got], [want for _, want in cases])
        self.assertEqual(['ctx_used' in line for line in got], [False, False, True, True, True, True],
                         'the tokens used do not depend on the status')
        self.run_hook({**request, 'agent_id': 'a1', 'status': 'completed', 'token_usage': {'total_tokens': 5}}, ['codex'])
        self.assertEqual(len(self.lines()), len(cases), "a child's completed request writes nothing")

    def test_the_controller_refresh_read(self):
        # `codex-query GEN THREAD` takes both from validated argv: the confined
        # exec carries no environment.
        os.environ.pop('YOLOCTL_AGENT_GEN', None)
        self.assertEqual(agent_observe.main(['codex-query', '3', SID]), 0, 'no daemon writes nothing')
        self.assertEqual(self.lines(), [])
        d = self.daemon(thread={'id': SID, 'model': 'gpt-5.6-luna', 'reasoningEffort': 'high',
                                'status': {'type': 'active', 'activeFlags': ['waitingOnUserInput']}})
        self.assertEqual(agent_observe.main(['codex-query', '3', SID]), 0)
        query, = self.lines()
        self.assertEqual((query['event'], query['session_id'], query['model'], query['effort'], query['state']),
                         ('query', SID, 'gpt-5.6-luna', 'high', 'waiting_input'), 'labelled with the thread it read')
        self.assertEqual(d.reads, [{'threadId': SID, 'includeTurns': False}])

    def test_the_refresh_read_refuses_invalid_arguments(self):
        self.daemon()
        for args in (['codex-query'], ['codex-query', '3'], ['codex-query', '0', SID], ['codex-query', '03', SID],
                     ['codex-query', str(1 << 64), SID], ['codex-query', '3', SID.upper()],
                     ['codex-query', '3', 'not-a-uuid'], ['codex-query', '3', SID, 'extra'],
                     ['codex-query', '../3', SID]):
            with self.subTest(args):
                self.assertEqual(agent_observe.main(args), 2)
        self.assertEqual(self.lines(), [])

    def test_the_refresh_read_writes_only_an_existing_generation(self):
        self.daemon()
        self.assertEqual(agent_observe.main(['codex-query', '4', SID]), 0)
        self.assertFalse((self.root / 'gen-4').exists(), 'the writer never creates a generation directory')

    def test_child_agents_never_set_box_state_or_context(self):
        self.run_hook({'hook_event_name': 'UserPromptSubmit', 'session_id': SID, 'agent_id': 'a1', 'prompt': 'x'}, ['codex'])
        self.run_hook({'hook_event_name': 'PostModelRequest', 'session_id': SID, 'agent_type': 'worker', 'status': 'failed',
                       'token_usage': {'total_tokens': 5}}, ['codex'])
        self.run_hook({'hook_event_name': 'PermissionRequest', 'session_id': SID, 'agent_id': 'a1', 'tool_name': 'shell'},
                      ['codex'])
        permission, = self.lines()
        self.assertEqual(set(permission) - {'pid', 'ts', 'dur_ms'}, {'v', 'event', 'state'},
                         "a child's permission wait is the box's, without the child's session")

    def test_no_query_for_other_events(self):
        d = self.daemon()
        self.run_hook({'hook_event_name': 'SessionEnd', 'session_id': SID}, ['codex'])
        self.run_hook({'hook_event_name': 'SubagentStart', 'session_id': SID}, ['codex'])
        self.assertEqual([line['event'] for line in self.lines()], ['SessionEnd', 'SubagentStart'])
        self.assertEqual(d.reads, [])

    def test_nothing_is_queried_without_a_trustworthy_daemon(self):
        cases = {
            'no daemon (a -c launch)': lambda: None,
            'not loaded': lambda: self.daemon(thread={'id': SID, 'model': 'm', 'reasoningEffort': 'high',
                                                      'status': {'type': 'notLoaded'}}),
            'another thread': lambda: self.daemon(thread={'id': '7c9e6679-7425-40de-944b-e07fc1f90ae7', 'model': 'm',
                                                          'reasoningEffort': 'high', 'status': {'type': 'idle'}}),
            'custom effort': lambda: self.daemon(thread={'id': SID, 'model': 'm', 'reasoningEffort': 'turbo',
                                                         'status': {'type': 'active', 'activeFlags': []}}),
            'no model': lambda: self.daemon(thread={'id': SID, 'reasoningEffort': 'high', 'status': {'type': 'idle'}}),
        }
        for name, make in cases.items():
            with self.subTest(name):
                for f in self.gen.glob('events.*'):
                    f.unlink()
                for f in self.dir.iterdir():
                    f.unlink()
                make()
                self.start()
                self.assertEqual([line['event'] for line in self.lines()], ['SessionStart'])

    def test_the_peer_must_be_this_users_codex_app_server(self):
        d = self.daemon()
        self.peer = (b'/usr/bin/python3', b'-m', b'fake')
        self.start()
        self.assertEqual([line['event'] for line in self.lines()], ['SessionStart'])
        self.assertEqual(d.reads, [], 'nothing is sent to an unbound peer')

    def test_the_daemon_directory_must_be_private_and_not_a_link(self):
        self.daemon()
        self.dir.chmod(0o755)
        self.start()
        self.dir.chmod(0o700)
        moved = self.dir.with_name('elsewhere')
        self.dir.rename(moved)
        self.dir.symlink_to(moved)
        self.start()
        self.assertEqual([line['event'] for line in self.lines()], ['SessionStart', 'SessionStart'])

    def test_a_resetting_socket_is_skipped_within_the_bound(self):
        self.daemon('0-reset', reset=True)
        self.daemon('5-control')
        self.start()
        self.assertEqual([line['event'] for line in self.lines()], ['SessionStart', 'query'])

    def test_a_slow_daemon_is_abandoned_at_the_bound(self):
        self.daemon(delay=2)
        started = time.monotonic()
        self.start()
        self.assertLess(time.monotonic() - started, 1.5)
        self.assertEqual([line['event'] for line in self.lines()], ['SessionStart'])


if __name__ == '__main__':
    unittest.main()
