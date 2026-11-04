"""Host protocol, isolation and failure tests. These do not simulate KVM speed."""

import importlib.util
import json
from pathlib import Path
import socket
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("fc_poc", Path(__file__).with_name("poc.py"))
poc = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(poc)


class PoCTests(unittest.TestCase):
    def test_ready_without_verified_confinement_is_rejected(self):
        request = {"nonce": "new-nonce", "generation": "new-generation"}
        response = {"ok": True, "stage": "controller-ready", "bundle": "build",
                    "nonce": request["nonce"], "generation": request["generation"], "accounts": ["trial"],
                    "landlock_abi": 6, "openshell_confinement_passed": True}
        poc.validate_gate(response, request, "build")
        for field, value in [("landlock_abi", 0), ("openshell_confinement_passed", False),
                             ("generation", "old-generation"), ("nonce", "old-nonce"),
                             ("bundle", "wrong-build"), ("accounts", [])]:
            with self.subTest(field=field):
                bad = dict(response, **{field: value})
                with self.assertRaisesRegex(RuntimeError, "gate failed"):
                    poc.validate_gate(bad, request, "build")

    def test_lima_baseline_is_read_only_and_excludes_failed_starts(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "timings.jsonl"
            text = '\n'.join(json.dumps(row) for row in [
                {"phase": "start-1", "seconds": 14, "exit_code": 0},
                {"phase": "start-2", "seconds": 100, "exit_code": 1},
                {"phase": "stop-1", "seconds": 1, "exit_code": 0}])
            path.write_text(text)
            result = poc.lima_baseline(tmp)
            self.assertEqual(result["start"], {"n": 1, "median_ms": 14000, "samples_ms": [14000]})
            self.assertIn("not an equivalent", result["scope"])
            self.assertEqual(path.read_text(), text)

    def test_existing_state_and_reports_are_never_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "existing"
            path.mkdir()
            (path / "account").write_text("untouched")
            with self.assertRaises(FileExistsError):
                poc.new_directory(path)
            poc.save_json(path / "report.json", {"old": True})
            with self.assertRaises(FileExistsError):
                poc.save_json(path / "report.json", {"old": False})
            self.assertEqual(json.loads((path / "report.json").read_text()), {"old": True})
            self.assertEqual((path / "account").read_text(), "untouched")

    def test_corrupted_cached_image_is_not_executed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "cache").mkdir()
            (root / "out").mkdir()
            (root / "cache/kernel-source.tar.gz").write_bytes(b"modified")
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                poc.download("kernel-source.tar.gz", root / "out", root / "cache")
            self.assertFalse((root / "out/kernel-source.tar.gz").exists())

    def test_archive_link_is_not_followed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            with tarfile.open(root / "input.tgz", "w:gz") as archive:
                entry = tarfile.TarInfo("binary")
                entry.type = tarfile.SYMTYPE
                entry.linkname = "/etc/passwd"
                archive.addfile(entry)
            with self.assertRaisesRegex(RuntimeError, "not a regular"):
                poc.tar_file(root / "input.tgz", "binary", root / "out")
            self.assertFalse((root / "out").exists())

    def test_failed_vm_launch_cleans_only_its_process(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            executable = root / "firecracker"
            executable.write_text("#!/bin/sh\nexit 17\n")
            executable.chmod(0o755)
            disk = root / "data.ext4"
            disk.touch()
            vm = poc.VM(root, root, disk, 1, 256, 0.1)
            with self.assertRaisesRegex(RuntimeError, "exited 17"):
                with vm:
                    self.fail("failed VMM reached ready")
            self.assertEqual(vm.proc.poll(), 17)
            self.assertFalse(vm.directory.exists())
            self.assertTrue(disk.exists())
            self.assertTrue((root / "serial.log").exists())

    def test_timeout_terminates_owned_child_and_preserves_logs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            executable = root / "firecracker"
            executable.write_text("#!/bin/sh\nexec sleep 20\n")
            executable.chmod(0o755)
            disk = root / "data.ext4"
            disk.touch()
            vm = poc.VM(root, root, disk, 1, 256, 0.02)
            with self.assertRaises(TimeoutError):
                with vm:
                    self.fail("silent child reached ready")
            self.assertIsNotNone(vm.proc.poll())
            self.assertFalse(vm.directory.exists())
            self.assertTrue((root / "serial.log").exists())

    def test_activation_sent_once_on_lost_reply(self):
        # Production sample calls guest exactly once for the mutating request;
        # a transport loss must escape instead of entering the status retry loop.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            nonce = "test-nonce"
            received = []
            errors = []
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            sock.bind(str(root / "vsock"))
            sock.listen()

            def server():
                try:
                    conn, _ = sock.accept()
                    with conn, conn.makefile("rwb", buffering=0) as stream:
                        self.assertEqual(stream.readline(), b"CONNECT 5252\n")
                        stream.write(b"OK 5252\n")
                        received.append(json.loads(stream.readline()))
                        # EOF before reply; do not accept another connection.
                except BaseException as exc:
                    errors.append(exc)

            thread = threading.Thread(target=server)
            thread.start()
            try:
                with self.assertRaisesRegex(RuntimeError, "truncated"):
                    poc.guest(root / "vsock", {"action": "activate", "nonce": nonce}, 1)
            finally:
                thread.join(timeout=2)
                sock.close()
            self.assertFalse(thread.is_alive())
            self.assertEqual(errors, [])
            self.assertEqual(received, [{"action": "activate", "nonce": nonce}])

    def test_no_kvm_fails_before_creating_trial_state(self):
        import argparse
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            args = argparse.Namespace(output=str(root / "trial"))
            with patch.object(poc, "require_platform"), patch.object(poc, "require_tools"), \
                    patch.object(poc.os, "open", side_effect=FileNotFoundError("no kvm")):
                with self.assertRaisesRegex(RuntimeError, "/dev/kvm"):
                    poc.bench(args)
            self.assertFalse((root / "trial").exists())

    def test_http_error_cannot_be_treated_as_snapshot_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "api"
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            sock.bind(str(path))
            sock.listen()

            def server():
                conn, _ = sock.accept()
                with conn, conn.makefile("rb") as stream:
                    stream.readline()
                    length = 0
                    while (line := stream.readline()) != b"\r\n":
                        if line.lower().startswith(b"content-length:"):
                            length = int(line.split(b":", 1)[1])
                    stream.read(length)
                    conn.sendall(b"HTTP/1.1 400 Bad Request\r\nContent-Length: 5\r\n\r\nerror")

            thread = threading.Thread(target=server)
            thread.start()
            try:
                with self.assertRaisesRegex(RuntimeError, "400"):
                    poc.api(path, "PUT", "/snapshot/load", {}, 1)
            finally:
                thread.join(timeout=2)
                sock.close()


if __name__ == "__main__":
    unittest.main()
