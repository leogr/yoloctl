"""Finite protocol fixtures for the privileged holder smoke; no VM or endpoint."""
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "privileged_session_smoke", Path(__file__).parent / "smoke" / "privileged-session.py"
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)

FAKE = r'''
import json, sys, time
mode = sys.argv[1]
print("fixture stderr", file=sys.stderr, flush=True)
if mode == "eof":
    sys.exit(0)
if mode == "no-ready":
    time.sleep(30)
if mode == "bad-json":
    print("not-json", flush=True)
    time.sleep(30)
ready = dict(type="ready", version=1, session="fixture-session")
if mode == "bad-ready":
    ready["session"] = ""
print(json.dumps(ready), flush=True)
screens = 0
shell = ""
for line in sys.stdin:
    request = json.loads(line)
    op = request["op"]
    reply = dict(id=request["id"], type=op)
    if mode == "wrong-id":
        reply["id"] = "wrong"
    if mode in ("exit", "disconnected", "error", "input_uncertain"):
        print(json.dumps(dict(type=mode, status=7, error="fixture error")), flush=True)
        time.sleep(30)
    if op == "text":
        shell = request["text"]
        reply["type"] = "sent"
    elif op == "key":
        reply["type"] = "sent"
    elif op == "screen":
        screens += 1
        marker = "privileged-" + shell.split()[-1]
        lines = [shell] if mode == "echo-only" or screens == 1 else [marker]
        reply["screen"] = dict(full=True, revision=screens, lines=lines)
        if mode == "bad-screen":
            reply["screen"]["full"] = False
    elif op == "wait":
        time.sleep(.01)
    elif op == "detach":
        if screens < 2:
            raise RuntimeError("detached before observing output")
        reply["type"] = "detached"
    print(json.dumps(reply), flush=True)
    if op == "detach":
        sys.exit(9 if mode == "bad-status" else 0)
'''


class PrivilegedSessionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.output = Path(directory.name)
        self.clients = []
        actual = smoke.RecordedClient

        def record(*args):
            client = actual(*args)
            self.clients.append(client)
            return client

        replacement = patch.object(smoke, "RecordedClient", record)
        replacement.start()
        self.addCleanup(replacement.stop)
        self.addCleanup(self.assert_closed)

    def assert_closed(self):
        for client in self.clients:
            self.assertIsNotNone(client.process.poll())
            self.assertFalse(client.reader.is_alive())
            self.assertTrue(client.log.closed)
            self.assertTrue(client.process.stdin.closed)
            self.assertTrue(client.process.stdout.closed)

    def run_mode(self, mode, timeout=2):
        return smoke.run_session([sys.executable, "-u", "-c", FAKE, mode], self.output, timeout)

    def test_observes_runtime_output_before_detach_and_retains_evidence(self):
        result = self.run_mode("ok")
        self.assertEqual(result["status"], "PASS")
        self.assertEqual(result["frontend_status"], 0)
        self.assertEqual(result["session"], "fixture-session")
        requests = [json.loads(line) for line in (self.output / "holder.requests.jsonl").read_text().splitlines()]
        self.assertEqual([r["op"] for r in requests], ["text", "key", "screen", "wait", "screen", "detach"])
        self.assertNotIn(result["marker"], requests[0]["text"])
        self.assertNotIn("exit", requests[0]["text"])
        self.assertEqual(requests[3]["since"], 1)
        replies = [json.loads(line) for line in (self.output / "holder.stdout.jsonl").read_text().splitlines()]
        self.assertEqual(replies[0]["type"], "ready")
        self.assertEqual(replies[-1]["type"], "detached")
        self.assertIn("fixture stderr", (self.output / "holder.stderr").read_text())
        self.assertEqual(json.loads((self.output / "holder-result.json").read_text()), result)

    def test_echo_only_times_out_without_detach_and_cleans_up(self):
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            self.run_mode("echo-only", .2)
        self.assertLess(time.monotonic() - started, 2)
        requests = (self.output / "holder.requests.jsonl").read_text()
        self.assertNotIn('"op": "detach"', requests)
        self.assertEqual(json.loads((self.output / "holder-result.json").read_text())["status"], "FAIL")

    def test_readiness_wait_is_also_bounded(self):
        started = time.monotonic()
        with self.assertRaises(TimeoutError):
            self.run_mode("no-ready", .15)
        self.assertLess(time.monotonic() - started, 2)

    def test_early_eof_exit_and_error_replies_fail(self):
        for mode in ("eof", "exit", "disconnected", "error", "input_uncertain"):
            with self.subTest(mode=mode), self.assertRaises((EOFError, RuntimeError)):
                self.run_mode(mode)

    def test_malformed_or_mismatched_protocol_fails(self):
        for mode in ("bad-ready", "wrong-id", "bad-screen", "bad-json"):
            with self.subTest(mode=mode), self.assertRaises((AssertionError, RuntimeError, ValueError)):
                self.run_mode(mode)
        self.assertEqual((self.output / "holder.stdout.jsonl").read_text(), "not-json\n")

    def test_nonzero_frontend_after_detach_fails(self):
        with self.assertRaisesRegex(AssertionError, "frontend exited 9"):
            self.run_mode("bad-status")


if __name__ == "__main__":
    unittest.main()
