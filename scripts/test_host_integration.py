"""Host integration smoke probe tests on local PTYs, without a desktop or Lima."""
import hashlib
import importlib.util
import os
import shlex
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).parent / "smoke" / "host-integration.py"
spec = importlib.util.spec_from_file_location("host_integration", SCRIPT)
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)


class HostIntegrationTests(unittest.TestCase):
    def test_staged_probe_preserves_arguments_and_retained_browser(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            audit = root / "a probe's \u2603 audit.jsonl"
            token = "token-" * 80 + "';$(false)\u2603"
            browser = "/tmp/" + "long-path/" * 40 + "a browser's $(false)\u2603"
            source = SCRIPT.with_name("host-integration-probe.py").read_bytes()
            command = host.mixed.prepare_python_probe([], source, str(audit), token,
                                                      env={"BROWSER": browser}, directory=str(root))
            # The terminal receives only a short file launch, without the source,
            # argv or environment appearing as shell input or echoing readiness.
            self.assertLess(len(command), 255)
            self.assertEqual(command.count(b"\r"), 1)
            self.assertNotIn(b"HOST_READY_", command)
            driver = host.mixed.Driver(sys.executable, "test", "test", root / "out")
            try:
                view = driver.start("shell", ["bash", "--noprofile", "--norc", "-i"])
                driver.send(view, command)
                driver.wait("staged probe", lambda: bool(host.events(audit)), timeout=5)
                self.assertEqual(host.events(audit)[0]["browser_path"], browser)
                driver.wait("exact token", lambda: b"HOST_READY_" + token.encode() in view.tail, timeout=5)
                driver.send(view, b"pq")
                driver.wait("exec exit", lambda: view.status is not None, timeout=5)
                self.assertEqual(view.status, 0)
                self.assertEqual([e["event"] for e in host.events(audit)], ["ready", "ping", "done"])
            finally:
                driver.close()

    def test_guest_probe_audits_fragmented_image_and_browser_results(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            audit, image = root / "audit.jsonl", root / "a copied image's file.png"
            image.write_bytes(host.png_fixture())
            browser = root / "browser"
            browser.write_text('#!/bin/sh\ncase "$1" in https://*) exit 0;; *) exit 1;; esac\n')
            browser.chmod(0o700)
            driver = host.mixed.Driver(sys.executable, "test", "test", root / "out")
            with patch.dict(os.environ, BROWSER=str(browser)):
                view = driver.start("probe", [sys.executable, "-B", str(SCRIPT.with_name("host-integration-probe.py")), str(audit), "fixture"])
            try:
                driver.wait("ready", lambda: bool(host.events(audit)), timeout=5)
                for byte in b"\x1b[200~" + shlex.quote(str(image)).encode() + b"\x1b[201~":
                    driver.send(view, bytes([byte]))
                driver.send(view, b"\x16pb")
                driver.wait("first browser", lambda: any(e["event"] == "browser" for e in host.events(audit)), timeout=5)
                driver.send(view, b"i")
                driver.wait("rejected browser", lambda: any(e.get("request") == "i" for e in host.events(audit)), timeout=5)
                driver.send(view, b"q")
                driver.wait("exit", lambda: view.status is not None, timeout=5)
                self.assertEqual(view.status, 0)
                events = host.events(audit)
                self.assertEqual([e["event"] for e in events], ["ready", "image", "ctrl-v", "ping", "browser", "browser", "done"])
                self.assertEqual(events[1]["sha256"], hashlib.sha256(image.read_bytes()).hexdigest())
                self.assertEqual([e["status"] for e in events if e["event"] == "browser"], [0, 1])
            finally:
                driver.close()

    def test_partial_audit_records_do_not_count_as_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "audit"
            path.write_bytes(b'{"event":"ready"}\n{"event":"image"')
            self.assertEqual(host.events(path), [{"event": "ready"}])


if __name__ == "__main__":
    unittest.main()
