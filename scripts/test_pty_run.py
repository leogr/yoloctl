"""PTY smoke harness regressions; no VM or user configuration is involved."""

import errno
import importlib.util
import io
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


HARNESS = Path(__file__).parent / "smoke" / "pty-run.py"
spec = importlib.util.spec_from_file_location("pty_run", HARNESS)
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class PTYRunnerTests(unittest.TestCase):
    def test_attach_smoke_fixture_retains_ready_marker(self):
        # Exercise the ACTUAL smoke commands against readline. A long title
        # and colored default prompt must not evict readiness from its 128-byte
        # replay tail. Marker text must also not match the echoed input early.
        script = HARNESS.with_name("box-attach.sh")

        def fixture_input(function):
            return subprocess.check_output(
                ["bash", "-c", 'source "$1"; "$2"', "_", str(script), function],
                timeout=5,
            )

        setup = fixture_input("smoke_replay_setup_input")
        resume = fixture_input("smoke_replay_resume_input")
        self.assertTrue(setup.endswith(b"\r"))
        self.assertTrue(resume.endswith(b"\r"))
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)
        self.addCleanup(os.close, slave)
        env = dict(os.environ, TERM="xterm-256color", INPUTRC="/dev/null",
                   HISTFILE="/dev/null", PS1="\\[\\e[32m\\]sandbox@probe$ \\[\\e[0m\\]",
                   PROMPT_COMMAND="printf '\\033]0;%0256d\\007' 0")
        # Retain a slave without a controlling terminal so Darwin cannot
        # revoke/flush the last output when the shell exits. The test concerns
        # readline bytes, not job control (the live smoke supplies that).
        child = subprocess.Popen(["bash", "--noprofile", "--norc", "-i"],
                                 stdin=slave, stdout=slave, stderr=slave, env=env,
                                 start_new_session=True)

        def stop():
            if child.poll() is None:
                child.kill()
            child.wait(timeout=5)

        self.addCleanup(stop)

        def read_until(marker):
            output = bytearray()
            deadline = time.monotonic() + 5
            while marker not in output:
                if time.monotonic() >= deadline:
                    self.fail(f"missing {marker!r}; output={bytes(output)!r}")
                ready, _, _ = select.select([master], [], [], 0.1)
                if ready:
                    output.extend(harness.read_pty(master))
            return bytes(output)

        read_until(b"sandbox@probe$")
        os.write(master, setup)
        output = read_until(b"REPLAY_MARKER")
        # Drain subsequent prompt/title bytes before checking the byte tail.
        # Bash has no running command here; it is waiting for the next input.
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if not select.select([master], [], [], 0.1)[0]:
                break
            output += harness.read_pty(master)
        else:
            self.fail("fixture kept writing instead of waiting at its prompt")
        self.assertIn(b"REPLAY_MARKER", output[-128:])
        self.assertNotIn(b"\x1b[>3u", output[-128:])
        self.assertNotIn(b"STATE_HEADER", output[-128:])
        self.assertNotIn(b"REPLAY_MARKER", setup)
        self.assertNotIn(b"REATTACH_OK", resume)
        os.write(master, resume)
        read_until(b"REATTACH_OK")
        self.assertEqual(child.wait(timeout=5), 0)

    def test_exit_between_select_and_waitpid_keeps_final_output(self):
        """Schedule the formerly lossy interleaving with a real PTY child."""
        master, slave = pty.openpty()
        self.addCleanup(os.close, slave)

        def fork_with_retained_slave():
            # Darwin drains/revokes a controlling terminal when its session
            # leader exits, and flushes it on last-slave close. Our deliberately
            # blocking waitpid prevents concurrent draining. Isolate the
            # reader/reaper race: use real PTY stdio without a controlling tty,
            # and retain the parent's slave so output survives the child exit.
            pid = os.fork()
            if pid == 0:
                os.close(master)
                os.setsid()  # Keep harness timeout killpg(pid) effective.
                for fd in (0, 1, 2):
                    os.dup2(slave, fd)
                if slave > 2:
                    os.close(slave)
            return pid, master

        trigger, release = os.pipe()
        self.addCleanup(os.close, trigger)
        self.addCleanup(os.close, release)
        os.set_inheritable(trigger, True)
        child = (
            f"import os; os.read({trigger}, 1); "
            "os.write(1, b'FINAL_REATTACH_HINT\\n'); os._exit(7)"
        )
        actual_select = harness.select.select
        actual_waitpid = os.waitpid
        released = False

        def select_then_exit(readers, writers, errors, timeout):
            nonlocal released
            # The child cannot produce output before this readiness sample.
            result = actual_select(readers, writers, errors, 0)
            if not released:
                self.assertFalse(result[0])
                os.write(release, b"x")
                released = True
                return [], [], []
            return result

        def await_scheduled_exit(pid, flags):
            # Synchronize only the race window; the child does real PTY I/O.
            return actual_waitpid(pid, 0 if released else flags)

        for sig in (signal.SIGINT, signal.SIGTERM):
            self.addCleanup(signal.signal, sig, signal.getsignal(sig))
        stdin = io.TextIOWrapper(io.BytesIO())
        stdout = io.TextIOWrapper(io.BytesIO())
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "output"
            argv = [str(HARNESS), "--output", str(output), "--timeout", "5",
                    "--", sys.executable, "-c", child]
            with patch.object(sys, "argv", argv), \
                    patch.object(sys, "stdin", stdin), \
                    patch.object(sys, "stdout", stdout), \
                    patch.object(harness.pty, "fork", fork_with_retained_slave), \
                    patch.object(harness.select, "select", select_then_exit), \
                    patch.object(harness.os, "waitpid", await_scheduled_exit):
                code = harness.main()
            self.assertEqual(code, 7)
            self.assertTrue(released)
            self.assertIn(b"FINAL_REATTACH_HINT", output.read_bytes())

    def test_real_command_output_and_exit_status(self):
        # Keep an unmocked forkpty path alongside the scheduled reader/reaper
        # fixture, including the platform's controlling-terminal teardown.
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "output"
            result = subprocess.run(
                [sys.executable, "-B", str(HARNESS), "--timeout", "2",
                 "--output", str(output), "--", sys.executable, "-c",
                 "import os; os.write(1, b'STDOUT_MARKER\\n'); "
                 "os.write(2, b'STDERR_MARKER\\n'); os._exit(7)"],
                input=b"", capture_output=True, timeout=5, check=False,
            )
            self.assertEqual(result.returncode, 7, result.stderr)
            self.assertIn(b"STDOUT_MARKER", output.read_bytes())
            self.assertIn(b"STDERR_MARKER", output.read_bytes())
            self.assertIn(b"STDOUT_MARKER", result.stdout)
            self.assertIn(b"STDERR_MARKER", result.stdout)

    def test_terminal_controls_stay_in_capture_even_on_timeout(self):
        # This is a headless harness, not a terminal proxy. Forwarding these
        # bytes to its caller can enable reporting modes and queue query
        # replies in the caller's shell, especially when the child is killed.
        payload = (b"READY\x1b[?1049h\x1b[?1003h\x1b[?1006h\x1b[?1004h"
                   b"\x1b[>31u\x1b]4;0;?\x07\x1b]52;c;Y29weQ==\x1b\\"
                   b"\x9b?1003h\x08\r\xc3\xa9\n")
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "output"
            result = subprocess.run(
                [sys.executable, "-B", str(HARNESS), "--timeout", "2",
                 "--output", str(output), "--", sys.executable, "-c",
                 "import os,time,tty; tty.setraw(0); "
                 f"os.write(1, {payload!r}); time.sleep(30)"],
                input=b"", capture_output=True, timeout=5, check=False,
            )
            self.assertEqual(result.returncode, 124, result.stderr)
            self.assertEqual(output.read_bytes(), payload)
            self.assertIn(b"READY", result.stdout)
            self.assertIn(b"\\x1b[?1003h", result.stdout)
            self.assertTrue(all(c == 10 or 32 <= c < 127 for c in result.stdout),
                            f"executable terminal controls leaked: {result.stdout!r}")

    def test_drain_does_not_wait_for_inherited_slave(self):
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)
        self.addCleanup(os.close, slave)
        os.write(slave, b"queued output\n")
        captured = []
        harness.drain_pty(master, captured.append)
        self.assertIn(b"queued output", b"".join(captured))
        # The slave is still open; an empty drain must also return promptly.
        harness.drain_pty(master, captured.append)

    def test_drain_is_bounded_with_a_continuous_writer(self):
        with patch.object(harness.os, "set_blocking"), \
                patch.object(harness, "read_pty", return_value=b"more"), \
                patch.object(harness.time, "monotonic", side_effect=[0, 0.1, 0.2, 0.3]):
            captured = []
            harness.drain_pty(42, captured.append)
        self.assertEqual(captured, [b"more", b"more"])

    def test_linux_eio_and_macos_eof(self):
        for result in (b"", OSError(errno.EIO, "closed slave")):
            with self.subTest(result=result):
                kwargs = {"side_effect": result} if isinstance(result, OSError) else {"return_value": result}
                with patch.object(harness.os, "read", **kwargs):
                    self.assertEqual(harness.read_pty(42), b"")
        with patch.object(harness.os, "read", side_effect=OSError(errno.EBADF, "bad fd")):
            with self.assertRaises(OSError):
                harness.read_pty(42)

    def test_timeout_kills_and_reaps_child(self):
        with tempfile.TemporaryDirectory() as temp:
            result = subprocess.run(
                [sys.executable, "-B", str(HARNESS), "--timeout", "0.1",
                 "--output", str(Path(temp) / "output"), "--pid-file", str(Path(temp) / "pid"),
                 "--", sys.executable, "-c", "import time; time.sleep(30)"],
                input=b"", capture_output=True, timeout=5, check=False,
            )
            self.assertEqual(result.returncode, 124, result.stderr)
            pid = int((Path(temp) / "pid").read_text())
            with self.assertRaises(ProcessLookupError):
                os.kill(pid, 0)

    def test_closed_slave_does_not_bypass_timeout(self):
        # EOF must not turn into an unbounded blocking waitpid while the
        # process keeps running after closing its terminal descriptors.
        with tempfile.TemporaryDirectory() as temp:
            result = subprocess.run(
                [sys.executable, "-B", str(HARNESS), "--timeout", "0.2",
                 "--output", str(Path(temp) / "output"), "--", sys.executable, "-c",
                 "import os,time; os.close(0); os.close(1); os.close(2); time.sleep(30)"],
                input=b"", capture_output=True, timeout=5, check=False,
            )
            self.assertEqual(result.returncode, 124, result.stderr)


if __name__ == "__main__":
    unittest.main()
