"""Local fixture-state regressions; no VM, root, real forward or namespace test."""
import importlib.util
import io
import http.server
import sys
import threading
from types import SimpleNamespace
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location(
    'privileged_runtime_smoke', Path(__file__).parent / 'smoke' / 'privileged-runtime.py'
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class Process:
    def __init__(self, fixture, command, **kwargs):
        self.fixture = fixture
        fixture.process = self
        fixture.server_command = command
        fixture.marker = command[-1]
        fixture.events.append('launch')
        self.returncode = 9 if fixture.mode == 'early-exit' else None
        self.stdout, self.stderr = kwargs['stdout'], kwargs['stderr']
        self.stdin = Mock()
        self.stdin.write.side_effect = lambda value: fixture.events.append('clean-stop')
        self.stdin.flush.return_value = None
        self.stdin.close.return_value = None
        self.stderr.write(b'fixture server diagnostic\n')

    def poll(self):
        return self.returncode

    def terminate(self):
        self.fixture.events.append('terminate')
        self.returncode = -15

    def kill(self):
        self.fixture.events.append('kill')
        self.returncode = -9

    def wait(self, timeout):
        self.fixture.events.append('wait-owned')
        if self.returncode is None:
            if self.fixture.mode == 'stop-timeout':
                raise subprocess.TimeoutExpired('serving exec', timeout)
            self.returncode = 0
        return self.returncode


class PortFixtureTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.output = Path(directory.name)
        self.events = []
        self.mode = 'ok'
        self.process = None
        self.calls = []
        # All processes/probes are fakes: only a requested retry sleep should
        # consume their budget, not host scheduling or temporary-file I/O.
        self.now = 0.0
        popen = patch.object(smoke.subprocess, 'Popen', lambda command, **kw: Process(self, command, **kw))
        run = patch.object(smoke, 'capture', self.run_command)
        clock = patch.object(smoke.time, 'monotonic', lambda: self.now)
        sleep = patch.object(smoke.time, 'sleep', self.advance_time)
        popen.start(); run.start(); clock.start(); sleep.start()
        self.addCleanup(popen.stop); self.addCleanup(run.stop)
        self.addCleanup(clock.stop); self.addCleanup(sleep.stop)

    def advance_time(self, seconds):
        self.assertGreaterEqual(seconds, 0)
        self.now += seconds

    def run_command(self, command, output, label, timeout, *, controller=False):
        self.calls.append(command)
        self.assertGreater(timeout, 0)
        self.assertLessEqual(timeout, 10)
        if 'curl' not in command:
            self.events.append('diagnostic')
            return subprocess.CompletedProcess(command, 0, 'fixture diagnostic', '')
        self.assertIn('--noproxy', command)
        self.assertIn('--max-time', command)
        local = command[0] == 'cli'
        self.assertEqual(controller, local)
        if self.process.returncode is not None:
            self.events.append('cleanup-probe')
            return subprocess.CompletedProcess(command, 1 if self.mode == 'cleanup-unknown' else 7, '', 'closed')
        self.events.append('local' if local else 'host')
        if self.mode == 'recovering' and self.events.count('local' if local else 'host') == 1:
            return subprocess.CompletedProcess(command, 7, '', 'not ready yet')
        if self.mode == 'local-deadline' and local:
            raise smoke.ControllerInterrupted(smoke.receipt_hint())
        if self.mode == 'local-failure' and local:
            return subprocess.CompletedProcess(command, 7, '', 'no listener')
        if self.mode == 'host-timeout' and not local:
            return None
        if self.mode == 'host-failure' and not local:
            return subprocess.CompletedProcess(command, 52, '', 'empty reply')
        body = 'another server' if self.mode == 'wrong-body' and not local else self.marker
        return subprocess.CompletedProcess(command, 0, body, '')

    def execute(self, timeout=.05):
        return smoke.port_fixture('cli', 'fixture-space', 'fixture-box', 18471, self.output, timeout=timeout)

    def assert_owned_closed(self):
        self.assertIsNotNone(self.process.returncode)
        self.assertTrue(self.process.stdout.closed)
        self.assertTrue(self.process.stderr.closed)

    def test_local_readiness_precedes_real_forward_assertion_then_owned_cleanup(self):
        result = self.execute()
        self.assertEqual(result['status'], 'PASS')
        self.assertEqual(self.events, ['launch', 'local', 'host', 'clean-stop', 'wait-owned', 'cleanup-probe'])
        guest = self.server_command[self.server_command.index('--') + 1:]
        self.assertEqual(guest[:3], ['python3', '-u', '-c'])
        self.assertNotIn('setsid', guest)
        self.assertNotIn('&', guest)
        self.assert_owned_closed()
        self.assertIn('fixture server diagnostic', (self.output / 'server.stderr').read_text())

    def test_transient_readiness_waits_for_local_then_forwarded_response(self):
        self.mode = 'recovering'
        result = self.execute(timeout=.5)
        self.assertEqual(result['status'], 'PASS')
        self.assertEqual(self.events, ['launch', 'local', 'local', 'host', 'host',
                                       'clean-stop', 'wait-owned', 'cleanup-probe'])
        self.assert_owned_closed()

    def test_dead_listener_never_attempts_forward_and_preserves_failure(self):
        for mode in ('early-exit', 'local-failure'):
            with self.subTest(mode=mode):
                self.mode, self.events = mode, []
                with self.assertRaises((RuntimeError, TimeoutError)):
                    self.execute()
                self.assertNotIn('host', self.events)
                self.assertIn('diagnostic', self.events)
                self.assert_owned_closed()
                result = json.loads((self.output / 'port-result.json').read_text())
                self.assertFalse(result['local_ready'])
                self.assertEqual(result['status'], 'FAIL')

    def test_local_ready_does_not_mask_forward_failure_wrong_body_or_timeout(self):
        for mode in ('host-failure', 'wrong-body', 'host-timeout'):
            with self.subTest(mode=mode):
                self.mode, self.events = mode, []
                with self.assertRaises(TimeoutError):
                    self.execute()
                self.assertIn('host', self.events)
                self.assertLess(self.events.index('local'), self.events.index('host'))
                self.assertLess(self.events.index('diagnostic'), self.events.index('clean-stop'))
                result = json.loads((self.output / 'port-result.json').read_text())
                self.assertTrue(result['local_ready'])
                self.assertEqual(result['phase'], 'host-forward')
                self.assertEqual(result['status'], 'FAIL')
                self.assert_owned_closed()

    def test_controller_probe_deadline_aborts_without_forward_or_new_exec(self):
        self.mode = 'local-deadline'
        with self.assertRaisesRegex(smoke.ControllerInterrupted, 'space operation SPACE'):
            self.execute()
        self.assertNotIn('host', self.events)
        self.assertEqual(self.events.count('local'), 1)
        self.assertNotIn('cleanup-probe', self.events)
        # Only read-only box logs are allowed after the uncertain probe.
        self.assertFalse(any('ss' in call for call in self.calls))
        self.assert_owned_closed()

    def test_failed_server_stop_does_not_continue_to_next_phase(self):
        self.mode = 'stop-timeout'
        with self.assertRaisesRegex(smoke.ControllerInterrupted, 'space operation SPACE'):
            self.execute()
        self.assertNotIn('cleanup-probe', self.events)
        report = json.loads((self.output / 'port-result.json').read_text())
        self.assertEqual(report['status'], 'FAIL')
        self.assertTrue(report['controller_interrupted'])
        self.assert_owned_closed()

    def test_cleanup_exec_error_is_not_proof_of_listener_shutdown(self):
        self.mode = 'cleanup-unknown'
        # Run the real cleanup deadline on the fixture clock.
        with self.assertRaises(TimeoutError):
            self.execute()
        result = json.loads((self.output / 'port-result.json').read_text())
        self.assertEqual(result['phase'], 'listener-cleanup')
        self.assertEqual(result['status'], 'FAIL')
        self.assert_owned_closed()


class CleanupTests(unittest.TestCase):
    def test_guest_server_accepts_only_clean_stop_and_always_closes_listener(self):
        for stop in (b'stop\n', b''):
            with self.subTest(stop=stop):
                server, thread = Mock(), Mock()
                with patch.object(http.server, 'HTTPServer', return_value=server), \
                     patch.object(threading, 'Thread', return_value=thread), \
                     patch.object(sys, 'argv', ['fixture', 'unique-body']), \
                     patch.object(sys, 'stdin', SimpleNamespace(buffer=io.BytesIO(stop))), \
                     patch('builtins.print'):
                    if stop:
                        exec(compile(smoke.HTTP_SERVER, 'http-fixture', 'exec'), {})
                    else:
                        with self.assertRaisesRegex(RuntimeError, 'stop handshake'):
                            exec(compile(smoke.HTTP_SERVER, 'http-fixture', 'exec'), {})
                thread.start.assert_called_once_with()
                server.shutdown.assert_called_once_with()
                thread.join.assert_called_once_with()
                server.server_close.assert_called_once_with()

    def test_failed_clean_stop_is_fatal_even_when_forced_cleanup_reaps_client(self):
        process = Mock()
        process.poll.return_value = None
        process.wait.side_effect = [subprocess.TimeoutExpired('owned', 5),
                                    subprocess.TimeoutExpired('owned', 5), -9]
        with self.assertRaisesRegex(smoke.ControllerInterrupted, 'space operation SPACE'):
            smoke.stop_owned(process)
        process.stdin.write.assert_called_once_with(b'stop\n')
        process.terminate.assert_called_once_with()
        process.kill.assert_called_once_with()
        self.assertEqual(process.wait.call_count, 3)
        for call in process.wait.call_args_list:
            self.assertEqual(call.kwargs, {'timeout': 5})

    def test_controller_deadline_forwards_term_and_fails_without_replay(self):
        for forced in (False, True):
            with self.subTest(forced=forced), tempfile.TemporaryDirectory() as directory:
                process = Mock(returncode=143, pid=123)
                effects = [subprocess.TimeoutExpired('probe', 3)]
                if forced:
                    effects.append(subprocess.TimeoutExpired('probe', 5))
                effects.append(('captured stdout', 'captured stderr'))
                process.communicate.side_effect = effects
                with patch.object(smoke.subprocess, 'Popen', return_value=process) as spawn:
                    with self.assertRaisesRegex(smoke.ControllerInterrupted, 'do not retry'):
                        smoke.capture(['cli', 'box', 'exec'], Path(directory), 'probe', 3, controller=True)
                spawn.assert_called_once()
                process.terminate.assert_called_once_with()
                self.assertEqual(process.kill.call_count, int(forced))
                record = json.loads((Path(directory) / 'probe.jsonl').read_text())
                self.assertTrue(record['interrupted'])
                self.assertEqual(record['forced_client_kill'], forced)
                self.assertEqual(record['stdout'], 'captured stdout')

    def test_completed_controller_probe_is_not_signalled(self):
        process = Mock(returncode=0, pid=123)
        process.communicate.return_value = ('body', '')
        with tempfile.TemporaryDirectory() as directory, patch.object(smoke.subprocess, 'Popen', return_value=process):
            result = smoke.capture(['cli'], Path(directory), 'probe', 3, controller=True)
        self.assertEqual(result.stdout, 'body')
        process.terminate.assert_not_called()
        process.kill.assert_not_called()

    def test_timed_out_request_keeps_partial_diagnostics(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            error = subprocess.TimeoutExpired(['cli'], .1, output=b'partial stdout', stderr=b'partial stderr')
            with patch.object(smoke.subprocess, 'run', side_effect=error):
                self.assertIsNone(smoke.capture(['cli'], output, 'timeout', .1))
            record = json.loads((output / 'timeout.jsonl').read_text())
            self.assertEqual(record['stdout'], 'partial stdout')
            self.assertEqual(record['stderr'], 'partial stderr')
            self.assertEqual(record['error'], 'command timed out')


class RootFixtureTests(unittest.TestCase):
    def setUp(self):
        # Execute only definitions from the exact guest program. No process,
        # signal or /proc access occurs unless a test explicitly mocks it.
        self.guest = {'__name__': 'fixture_definitions'}
        exec(compile(smoke.ROOT_LAUNCHER, 'root-fixture', 'exec'), self.guest)
        self.record = dict(pid=4321, pgid=4321, sid=4321, state='S',
                           start_ticks=123, uids=[0, 0, 0, 0], ignored=0x4001)
        self.child = Mock(pid=4321)
        self.child.poll.return_value = None

    def test_exec_barrier_then_exact_child_evidence_before_success(self):
        snapshot = Mock(return_value=self.record.copy())
        self.guest['child_snapshot'] = snapshot
        with patch.object(smoke.subprocess, 'Popen', return_value=self.child) as spawn, patch('builtins.print') as printed:
            self.guest['launch']()
        snapshot.assert_called_once_with(4321)
        args, options = spawn.call_args
        self.assertEqual(args, (['sleep', '600'],))
        self.assertTrue(options['start_new_session'])
        self.assertTrue(options['close_fds'])
        for field in ('stdin', 'stdout', 'stderr'):
            self.assertEqual(options[field], subprocess.DEVNULL)
        with patch.object(self.guest['signal'], 'signal') as signals:
            options['preexec_fn']()
        self.assertEqual(signals.call_count, 2)
        self.assertEqual(signals.call_args_list[0].args, (self.guest['signal'].SIGHUP, self.guest['signal'].SIG_IGN))
        self.assertEqual(signals.call_args_list[1].args, (self.guest['signal'].SIGTERM, self.guest['signal'].SIG_IGN))
        receipt = json.loads(printed.call_args.args[0])
        self.assertEqual(receipt, dict(self.record, status='READY'))
        self.child.kill.assert_not_called()
        self.child.wait.assert_not_called()

    def test_missing_escape_root_ignore_or_liveness_evidence_kills_only_owned_child(self):
        for field, value in [('pgid', 12), ('sid', 12), ('uids', [0, 1000, 0, 0]),
                             ('ignored', 1), ('ignored', 0x4000), ('state', 'Z'), ('start_ticks', 0)]:
            with self.subTest(field=field, value=value):
                self.child.reset_mock()
                self.guest['child_snapshot'] = Mock(return_value=dict(self.record, **{field: value}))
                with patch.object(smoke.subprocess, 'Popen', return_value=self.child), self.assertRaises(RuntimeError):
                    self.guest['launch']()
                self.child.kill.assert_called_once_with()
                self.child.wait.assert_called_once_with(timeout=5)
        self.child.reset_mock()
        self.child.poll.return_value = 0
        self.guest['child_snapshot'] = Mock(return_value=self.record.copy())
        with patch.object(smoke.subprocess, 'Popen', return_value=self.child), self.assertRaises(RuntimeError):
            self.guest['launch']()
        self.child.kill.assert_not_called()
        self.child.wait.assert_called_once_with(timeout=5)

    def test_proc_parser_uses_final_parenthesis_and_all_uid_fields(self):
        fields = ['S', '111', '4321', '4321'] + ['0'] * 15 + ['123']
        class ProcFile:
            def __init__(self, part=''):
                self.part = part
            def __truediv__(self, part):
                return ProcFile(part)
            def read_text(self):
                if self.part == 'stat':
                    return '4321 (fixture (sleep)) ' + ' '.join(fields)
                return 'Name:\tsleep\nUid:\t0\t0\t0\t0\nSigIgn:\t0000000000004001\n'
        with patch.object(self.guest['pathlib'], 'Path', return_value=ProcFile()):
            self.assertEqual(self.guest['child_snapshot'](4321), self.record)

    def test_host_requires_readiness_and_never_retries_uncertain_launch(self):
        ready = dict(self.record, status='READY')
        outcomes = [None, subprocess.CompletedProcess([], 1, '', 'sudo failed'),
                    subprocess.CompletedProcess([], 0, 'not json', ''),
                    subprocess.CompletedProcess([], 0, '{}', '')]
        with tempfile.TemporaryDirectory() as directory:
            for outcome in outcomes:
                with self.subTest(outcome=outcome), patch.object(smoke, 'capture', return_value=outcome) as call:
                    with self.assertRaises((RuntimeError, ValueError)):
                        smoke.root_fixture('cli', 'space', 'box', directory)
                    call.assert_called_once()
                    self.assertEqual(json.loads((Path(directory) / 'root-result.json').read_text())['status'], 'FAIL')
            result = subprocess.CompletedProcess([], 0, json.dumps(ready), '')
            with patch.object(smoke, 'capture', return_value=result) as call, patch('builtins.print'):
                report = smoke.root_fixture('cli', 'space', 'box', directory)
            self.assertEqual(report['child'], ready)
            command = call.call_args.args[0]
            self.assertEqual(command[command.index('--') + 1:-1], ['sudo', '-n', 'python3', '-c'])
            self.assertEqual(command[-1], smoke.ROOT_LAUNCHER)


if __name__ == '__main__':
    unittest.main()
