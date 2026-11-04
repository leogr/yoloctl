"""Local framing/cleanup checks for the live box-use smoke driver, without Lima."""

import importlib.util
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "box_use_smoke", Path(__file__).parent / "smoke" / "box-use.py"
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class BoxUseDriverTests(unittest.TestCase):
    def client(self, program):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        client = smoke.Client(
            [sys.executable, "-u", "-c", program],
            Path(directory.name) / "stderr",
        )
        self.addCleanup(client.close)
        return client

    def test_framing_keeps_unicode_and_correlates_ids(self):
        client = self.client("""
import json, sys
for line in sys.stdin:
    request = json.loads(line)
    print(json.dumps(dict(id=request['id'], type='sent', text=request['text'])))
""")
        self.assertEqual(client.request("text", text="漢字\nhello")["text"], "漢字\nhello")
        self.assertEqual(client.request("text", text="next")["id"], "2")

    def test_rejects_wrong_response_id(self):
        client = self.client("import sys; sys.stdin.readline(); print('{\"id\":\"wrong\",\"type\":\"sent\"}')")
        with self.assertRaisesRegex(RuntimeError, "unexpected response"):
            client.request("screen")

    def test_bad_json_and_eof_are_failures(self):
        client = self.client("print('not JSON')")
        with self.assertRaises(ValueError):
            client.receive()
        client = self.client("pass")
        with self.assertRaises(EOFError):
            client.receive()

    def test_read_timeout_and_cleanup(self):
        client = self.client("import time; time.sleep(60)")
        with self.assertRaises(TimeoutError):
            client.receive(timeout=.05)
        client.close()
        self.assertIsNotNone(client.process.poll())

    def test_finite_batch_requires_identity_correlated_results_and_detach(self):
        first = dict(session="same", client="first")
        second = dict(session="same", client="second")
        screen = dict(rows=30, cols=100, revision=7,
                      input=dict(last_peer="finite", peer_revision=1))
        records = [
            dict(type="ready", version=1, session="same", client="finite"),
            dict(id="before", type="screen", screen=screen),
            dict(id="prompt", type="sent"), dict(id="submit", type="sent"),
            dict(id="settle", type="wait", screen=screen, timed_out=True),
            dict(id="after", type="screen", screen=screen),
            dict(type="detached"),
        ]
        encode = lambda values: "".join(json.dumps(value)+"\n" for value in values)
        self.assertEqual(smoke.validate_finite_batch(encode(records), first, second), records[0])
        for index, field, value in [
            (0, "session", "other"), (0, "client", "first"),
            (2, "type", "input_uncertain"), (3, "id", "wrong"),
            (4, "screen", dict(rows=30, cols=100, revision=7)),
            (5, "screen", dict(rows=1, cols=100, revision=7, input={})),
            (6, "type", "exit"),
        ]:
            with self.subTest(index=index, field=field):
                invalid = copy.deepcopy(records)
                invalid[index][field] = value
                with self.assertRaises(RuntimeError):
                    smoke.validate_finite_batch(encode(invalid), first, second)
        with self.assertRaises(RuntimeError):
            smoke.validate_finite_batch(encode(records[:-1]), first, second)


if __name__ == "__main__":
    unittest.main()
