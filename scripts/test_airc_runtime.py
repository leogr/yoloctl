"""Local checks for the AIRC runtime smoke driver's read waits, without aircd."""

import asyncio
import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location(
    "airc_runtime_smoke", Path(__file__).parent / "smoke" / "airc-runtime.py"
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


def privmsg(text):
    return {"command": "PRIVMSG", "params": ["#work", text], "tags": {}}


class FakeReads:
    """Answers read requests from scripted batches and records each request."""

    def __init__(self, batches):
        self.batches = list(batches)
        self.requests = []

    async def __call__(self, client, value):
        self.requests.append(dict(value))
        assert value["op"] == "read", value
        if not self.batches:
            await asyncio.sleep(0.01)
            return {"events": [], "next": value["after"]}
        return self.batches.pop(0)


class Client:
    cursor = 0


class UntilAllTests(unittest.TestCase):
    def run_with(self, batches, coroutine):
        reads = FakeReads(batches)
        original = smoke.request
        smoke.request = reads
        try:
            return asyncio.run(coroutine), reads
        finally:
            smoke.request = original

    def test_markers_in_one_read_need_no_second_read(self):
        client = Client()
        batch = {"events": [privmsg("agent-0-visible-marker"), privmsg("agent-1-visible-marker")], "next": 2}
        found, reads = self.run_with([batch], smoke.until_all(client, ["agent-0-visible-marker", "agent-1-visible-marker"]))
        self.assertEqual(sorted(found), ["agent-0-visible-marker", "agent-1-visible-marker"])
        self.assertEqual(len(reads.requests), 1)
        self.assertEqual(client.cursor, 2)

    def test_markers_in_separate_reads_advance_the_cursor(self):
        client = Client()
        batches = [
            {"events": [{"command": "JOIN", "params": ["agent-1-visible-marker"], "tags": {}}, privmsg("agent-0-visible-marker")], "next": 2},
            {"events": [privmsg("agent-1-visible-marker")], "next": 3},
        ]
        found, reads = self.run_with(batches, smoke.until_all(client, ["agent-0-visible-marker", "agent-1-visible-marker"]))
        self.assertEqual(found["agent-1-visible-marker"]["command"], "PRIVMSG")
        self.assertEqual([r["after"] for r in reads.requests], [0, 2])
        self.assertEqual(client.cursor, 3)

    def test_single_marker_returns_its_event(self):
        client = Client()
        batch = {"events": [privmsg("other"), privmsg("human-steering-marker")], "next": 2}
        event, reads = self.run_with([batch], smoke.until(client, "human-steering-marker"))
        self.assertEqual(event["params"][-1], "human-steering-marker")
        self.assertEqual(len(reads.requests), 1)

    def test_missing_marker_fails_within_its_bound(self):
        client = Client()
        batch = {"events": [privmsg("agent-0-visible-marker")], "next": 1}
        with self.assertRaisesRegex(AssertionError, "agent-1-visible-marker"):
            self.run_with([batch], smoke.until_all(client, ["agent-0-visible-marker", "agent-1-visible-marker"], timeout=0.2))


VECTORS = json.loads((Path(__file__).parents[1] / "internal" / "chatui" / "testdata" / "message-references.json").read_text())


class SharedReferenceTests(unittest.TestCase):
    def test_the_driver_decodes_the_shared_vectors(self):
        for entry in VECTORS["valid"]:
            self.assertEqual(smoke.shared_msgid(entry["reference"]), entry["msgid"])
        for reference in [*VECTORS["invalid"], "@12"]:
            with self.assertRaises(AssertionError, msg=reference):
                smoke.shared_msgid(reference)

    def test_a_confirmed_send_yields_its_reference_and_token(self):
        output = "confirmed #work m:Ej5FZ-ibQtOkVkJmFBdAAA; send=5d5a\n"
        self.assertEqual(smoke.confirmed(output, "#work"), ("m:Ej5FZ-ibQtOkVkJmFBdAAA", "5d5a"))
        for output in ["confirmed #work @3; send=5d5a\n", "confirmed #other m:Ej5FZ-ibQtOkVkJmFBdAAA; send=5d5a", "uncertain #work; send=5d5a"]:
            with self.assertRaises(AssertionError, msg=output):
                smoke.confirmed(output, "#work")


if __name__ == "__main__":
    unittest.main()
