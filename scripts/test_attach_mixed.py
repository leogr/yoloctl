"""Mixed-attach smoke driver/probe regressions on real local PTYs, without Lima."""

import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sys
import tempfile
import termios
import threading
import unittest


SCRIPT = Path(__file__).parent / "smoke" / "attach-mixed.py"
spec = importlib.util.spec_from_file_location("attach_mixed", SCRIPT)
mixed = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mixed)


class MixedAttachTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        self.driver = mixed.Driver(sys.executable, "test", "test", self.root)
        self.addCleanup(self.driver.close)

    def test_guest_probe_records_ordered_input_resize_queries_and_overlap(self):
        audit = self.root / "audit.jsonl"
        probe = SCRIPT.with_name("attach-probe.py")
        view = self.driver.start("probe", [sys.executable, "-B", str(probe), str(audit), "test-token"])
        self.driver.wait("probe startup", lambda: b"MIXED_READY_test-token" in view.tail, timeout=5)

        def events():
            return mixed.audit_events(audit.read_bytes())

        self.driver.input(view, "abc")
        self.driver.input(view, "DEF")
        self.driver.input(view, "123")
        self.driver.query(view)
        self.driver.wait("query output", lambda: b"\x1b[6n" in view.tail, timeout=5)
        # The test supplies the holder's reply in two reads, not a GUI terminal.
        self.driver.send(view, b"\x1b[2;")
        self.driver.pump()
        self.assertFalse(any(e["event"] == "reply" for e in events()))
        self.driver.send(view, b"3R")
        self.driver.wait("fragmented reply", lambda: any(e["event"] == "reply" for e in events()), timeout=5)

        self.driver.send(view, b"+")
        view.resize(27, 84)
        self.driver.wait("resize", lambda: any(e["event"] == "resize" and
                         e["rows"] == 27 and e["cols"] == 84 for e in events()), timeout=5)
        # Fill the real PTY output buffer. The guest must keep reading input;
        # an accidental blocking flood loop would make this fence time out.
        view.reading = False
        self.driver.wait("flood starts", lambda: any(e["event"] == "flood-start" for e in events()), timeout=5)
        self.driver.input(view, "abc")
        self.driver.wait("input while output is unread", lambda:
                         sum(e["event"] == "input" for e in events()) == 96, timeout=5)
        view.reading = True
        self.driver.wait("bounded flood progress", lambda: any(e["event"] == "progress" for e in events()), timeout=5)
        self.driver.send(view, b"-!")
        self.driver.wait("probe exit", lambda: view.status is not None, timeout=5)
        self.assertEqual(view.status, 7)
        mixed.validate_audit(events(), self.driver.expected, final=True, queries_sent=1)
        self.assertTrue(any(e["event"] == "input" and e["flooding"] for e in events()))

    def test_unread_view_does_not_block_driver_and_children_are_reaped(self):
        slow = self.driver.start("slow", [sys.executable, "-c",
            'import os; data = b"\\x1b[?1003h" + b"x" * 65536\nwhile True: os.write(1, data)'])
        slow.reading = False
        healthy = self.driver.start("healthy", [sys.executable, "-c",
            'import os,time; os.write(1,b"HEALTHY"); time.sleep(30)'])
        self.driver.wait("healthy peer", lambda: b"HEALTHY" in healthy.tail, timeout=5)
        self.assertEqual(slow.received, 0)
        self.assertIsNone(slow.status)
        self.driver.close()
        for view in (slow, healthy):
            self.assertIsNotNone(view.status)
            with self.assertRaises(ProcessLookupError):
                os.kill(view.pid, 0)

    def test_flood_start_waits_for_guest_before_resizing(self):
        audit = self.root / "audit.jsonl"
        view = self.driver.start("probe", [sys.executable, "-B", str(SCRIPT.with_name("attach-probe.py")),
                                           str(audit), "barrier-token"])
        self.driver.wait("probe startup", lambda: b"MIXED_READY_barrier-token" in view.tail, timeout=5)
        self.driver.audit = lambda: mixed.audit_events(audit.read_bytes())

        # Input can be buffered while a resize travels on its independent
        # channel. Pause the real guest to hold the start byte unacknowledged.
        os.kill(view.pid, signal.SIGSTOP)
        _, status = os.waitpid(view.pid, os.WUNTRACED)
        self.assertTrue(os.WIFSTOPPED(status))
        sent, done = threading.Event(), threading.Event()
        original_send = self.driver.send
        errors = []

        def send(target, data):
            original_send(target, data)
            sent.set()

        def start():
            try:
                self.driver.start_flood(view, timeout=3)
            except BaseException as error:
                errors.append(error)
            finally:
                done.set()

        self.driver.send = send
        worker = threading.Thread(target=start)
        worker.start()
        try:
            self.assertTrue(sent.wait(5), "start byte was not queued")
            premature = done.wait(0.2)
            # Another viewer can resize before the guest processes stdin.
            view.resize(29, 81)
        finally:
            os.kill(view.pid, signal.SIGCONT)
            worker.join(5)
        self.assertFalse(worker.is_alive(), "guest acknowledgment did not release the driver")
        if errors:
            raise errors[0]
        self.assertFalse(premature, "driver resized before the guest acknowledged flood start")

        view.resize(27, 84)
        self.driver.wait("resize during acknowledged output", lambda: any(
            e["event"] == "resize" and e["rows"] == 27 and e["cols"] == 84 and e["flooding"]
            for e in self.driver.audit()), timeout=5)
        self.driver.send(view, b"!")
        self.driver.wait("probe exit", lambda: view.status is not None, timeout=5)
        self.assertEqual(view.status, 7)

    def rejoin_fixture(self, mode):
        driver = mixed.Driver(sys.executable, "test", "test", self.root / mode)
        self.addCleanup(driver.close)
        audit = self.root / (mode + ".jsonl")
        # A real PTY carrying valid fragmented paints, with independent input
        # receipts like the guest audit. The screen contains 0123456789, but
        # that substring never appears in the renderer's output bytes.
        program = r'''
import json, os, select, sys, tty
audit, mode = sys.argv[1:]
tty.setraw(0)
paint = b"\r01\x1b[?25l2345678\x1b[?25h9"
with open(audit, "w", buffering=1) as output:
    def record(event, **fields):
        output.write(json.dumps(dict(event=event, **fields)) + "\n")
    record("ready")
    os.write(1, b"FIXTURE_READY\n" + paint)
    queried, frame = False, 0
    while True:
        readers, _, _ = select.select([0], [], [], 0.01)
        if readers:
            data = os.read(0, 4096)
            if not data:
                sys.exit(0)
            for key in data.decode("ascii"):
                if key == "?":
                    record("query", flooding=True)
                    if mode != "missing-reply":
                        record("reply", row=2, col=3)
                    queried = True
                elif mode != "missing-input":
                    record("input", key=key, flooding=True)
        if queried:
            if mode == "exited":
                sys.exit(9)
            if mode != "stalled":
                frame += 1
                os.write(1, paint + f" {frame:08x}".encode("ascii"))
'''
        view = driver.start("renderer", [sys.executable, "-B", "-c", program, str(audit), mode])
        driver.wait("fixture startup", lambda: b"FIXTURE_READY" in view.tail, timeout=5)

        def events():
            result = mixed.audit_events(audit.read_bytes())
            mixed.validate_audit(result, driver.expected, queries_sent=driver.queries)
            return result

        driver.audit = events
        return driver, view

    def test_rejoin_accepts_fragmented_paints_after_guest_acknowledgment(self):
        driver, view = self.rejoin_fixture("fragmented")
        driver.check_live_rejoin(view, "abc", timeout=3)
        self.assertNotIn(b"0123456789", view.tail)
        self.assertIn(b"01\x1b[?25l2345678\x1b[?25h9", view.tail)
        self.assertEqual(sum(e["event"] == "input" for e in driver.audit()), 24)
        self.assertEqual(sum(e["event"] == "reply" for e in driver.audit()), 1)

    def test_rejoin_rejects_missing_delivery_stalled_output_and_exit(self):
        for mode, failure in (
            ("missing-input", "timeout waiting for rejoined viewer input and query reply"),
            ("missing-reply", "timeout waiting for rejoined viewer input and query reply"),
            ("stalled", "timeout waiting for rejoined viewer live output"),
            ("exited", "rejoined viewer exited 9"),
        ):
            with self.subTest(mode=mode):
                driver, view = self.rejoin_fixture(mode)
                self.assertGreater(view.received, 0, "fixture must already have setup/snapshot bytes")
                with self.assertRaisesRegex(mixed.Failure, failure):
                    driver.check_live_rejoin(view, "abc", timeout=1)
                driver.close()

    def test_staged_probe_survives_shell_input_modes_and_does_not_echo_readiness(self):
        # Output from a shell command can arrive before Bash re-enters
        # Readline. Exercise the exact smoke payload on both sides of that
        # transition. Source/argv/env must not enter the terminal's input queue.
        for mode in ("readline", "canonical"):
            with self.subTest(mode=mode):
                view = self.driver.start(mode, ["bash", "--noprofile", "--norc", "-i"])
                if mode == "canonical":
                    self.driver.send(view, b"kill -STOP $$\r")

                    def stopped():
                        pid, status = os.waitpid(view.pid, os.WNOHANG | os.WUNTRACED)
                        if pid:
                            self.assertTrue(os.WIFSTOPPED(status))
                        return bool(pid)

                    self.driver.wait("shell stopped before Readline", stopped, timeout=5)
                    self.assertTrue(termios.tcgetattr(view.master)[3] & termios.ICANON)
                else:
                    self.driver.wait("Readline", lambda: not (
                        termios.tcgetattr(view.master)[3] & termios.ICANON), timeout=5)

                audit = self.root / (mode + " audit with spaces.jsonl")
                command = mixed.prepare_python_probe([], SCRIPT.with_name("attach-probe.py").read_bytes(),
                                                     str(audit), "shell-token", directory=str(self.root))
                self.assertLess(len(command), 255)
                self.assertTrue(command.endswith(b"\r"))
                self.assertEqual(command.count(b"\r"), 1)
                self.assertNotIn(b"MIXED_READY_", command)
                if mode == "canonical":
                    try:
                        self.driver.send(view, command)
                        # Echo proves the line was processed while canonical,
                        # before Bash can consume it or change terminal modes.
                        self.driver.wait("canonical echo", lambda: command[-33:-1] in view.tail, timeout=5)
                    finally:
                        os.kill(view.pid, signal.SIGCONT)
                else:
                    self.driver.send(view, command)
                self.driver.wait("staged probe", lambda: b"MIXED_READY_shell-token" in view.tail, timeout=10)
                self.driver.send(view, b"+")
                self.driver.wait("changing output", lambda: len(set(re.findall(
                    rb"0123456789 ([0-9a-f]{8})", view.tail))) >= 2, timeout=5)
                self.driver.send(view, b"!")
                self.driver.wait("exec exit", lambda: view.status is not None, timeout=5)
                self.assertEqual(view.status, 7)
                mixed.validate_audit(mixed.audit_events(audit.read_bytes()), {}, final=True, queries_sent=0)

    def test_probe_setup_failure_is_reported(self):
        failing = [sys.executable, "-c", 'import sys; sys.stderr.write("setup denied"); sys.exit(23)']
        with self.assertRaisesRegex(mixed.Failure, "prepare probe exited 23.*setup denied"):
            mixed.prepare_python_probe(failing, b"raise RuntimeError('must not run')", directory=str(self.root))
        self.assertEqual(list(self.root.iterdir()), [])

    def test_probe_keeps_resize_received_during_previous_size_sample(self):
        audit, sampled = self.root / "audit.jsonl", self.root / "sampled"
        # Force the SIGWINCH callback during get_terminal_size, after its
        # sample is taken. The second real PTY resize must still be reported.
        program = r'''
import os, runpy, signal, sys, time
probe, audit, sampled = sys.argv[1:]
run = runpy.run_path(probe)["run"]
get_size = os.get_terminal_size
intercepted = False
def sample(fd):
    global intercepted
    size = get_size(fd)
    if tuple(size) == (81, 29) and not intercepted:
        intercepted = True
        with open(sampled, "w"):
            pass
        deadline = time.monotonic() + 5
        while get_size(fd) == size:
            if time.monotonic() >= deadline:
                raise RuntimeError("second resize never arrived")
            time.sleep(0.005)
        signal.getsignal(signal.SIGWINCH)(signal.SIGWINCH, None)
    return size
os.get_terminal_size = sample
sys.exit(run(audit, "resize-race", timeout=15))
'''
        view = self.driver.start("probe", [sys.executable, "-B", "-c", program,
            str(SCRIPT.with_name("attach-probe.py")), str(audit), str(sampled)])
        self.driver.wait("probe startup", lambda: b"MIXED_READY_resize-race" in view.tail, timeout=5)
        view.resize(29, 81)
        self.driver.wait("first size sampled", sampled.exists, timeout=5)
        view.resize(27, 84)
        self.driver.wait("resize received during sample", lambda: any(
            e["event"] == "resize" and e["rows"] == 27 and e["cols"] == 84
            for e in mixed.audit_events(audit.read_bytes())), timeout=5)
        self.driver.send(view, b"!")
        self.driver.wait("probe exit", lambda: view.status is not None, timeout=5)
        self.assertEqual(view.status, 7)

    def test_wait_deadline_is_bounded_even_after_pty_eof(self):
        self.driver.start("eof", [sys.executable, "-c",
            'import os,time; os.close(0); os.close(1); os.close(2); time.sleep(30)'])
        with self.assertRaisesRegex(mixed.Failure, "timeout waiting for never"):
            self.driver.wait("never", lambda: False, timeout=0.2)

    def test_audit_preserves_per_sender_order_without_imposing_global_order(self):
        events = [dict(event="input", key=key) for key in "aD1bE2cF3"]
        events += [dict(event="query"), dict(event="reply", row=2, col=3), dict(event="done")]
        mixed.validate_audit(events, dict(abc="abc", DEF="DEF", **{"123": "123"}),
                             final=True, queries_sent=1)
        payload = b"".join(json.dumps(e).encode() + b"\n" for e in events)
        self.assertEqual(mixed.audit_events(payload + b'{"event":'), events)

    def test_negative_controls_reject_loss_duplication_reordering_and_wrong_replies(self):
        valid = [dict(event="input", key=key) for key in "abc"]
        valid += [dict(event="query"), dict(event="reply", row=2, col=3), dict(event="done")]
        cases = {
            "lost input": valid[1:],
            "duplicated input": [valid[0]] + valid,
            "reordered input": [valid[1], valid[0]] + valid[2:],
            "unknown input": [dict(event="input", key="z")] + valid,
            "duplicated reply": valid[:4] + [valid[4]] + valid[4:],
            "missing reply": valid[:4] + valid[5:],
            "wrong cursor": valid[:4] + [dict(event="reply", row=3, col=2)] + valid[5:],
            "unsolicited query": [dict(event="query")] + valid,
            "missing completion": valid[:-1],
        }
        for name, events in cases.items():
            with self.subTest(name=name), self.assertRaises(mixed.Failure):
                mixed.validate_audit(events, {"abc": "abc"}, final=True, queries_sent=1)
        with self.assertRaisesRegex(mixed.Failure, "guest probe failed"):
            mixed.audit_events(b'{"event":"error","message":"broken"}\n')


if __name__ == "__main__":
    unittest.main()
