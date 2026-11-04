"""Safety and independent-storage negative controls for the research probe."""

import importlib.util
from contextlib import contextmanager
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("storage-contract.py")
SPEC = importlib.util.spec_from_file_location("storage_contract", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class LocalPeer:
    """Use real temporary files for narrow negative-control fault injection."""

    def __init__(self, name, worker):
        self.name, self.worker = name, worker

    def call(self, op, **arguments):
        return self.worker.dispatch({"op": op, **arguments})


@contextmanager
def prepared_peers():
    with tempfile.TemporaryDirectory(dir="/tmp") as root:
        workers = [MODULE.Worker(root, "storage-contract-" + "a" * 32) for _ in range(2)]
        try:
            peers = [LocalPeer(name, worker) for name, worker in zip(("a", "b"), workers)]
            for peer in peers:
                peer.call("prepare", create=peer is peers[0])
            yield peers
        finally:
            for worker in workers:
                worker.close()
                if worker.runfd is not None:
                    MODULE.os.close(worker.runfd)
                MODULE.os.close(worker.rootfd)


class StorageContractTest(unittest.TestCase):
    def invoke(self, directory, roots, report=None):
        endpoints = directory / "endpoints.json"
        endpoints.write_text(json.dumps([
            {"name": name, "root": str(root)}
            for name, root in zip(("a", "b"), roots)
        ]))
        arguments = [sys.executable, str(SCRIPT), "--endpoints", str(endpoints)]
        if report is not None:
            arguments += ["--report", str(report)]
        return subprocess.run(arguments, capture_output=True, text=True, timeout=30)

    def test_native_shared_storage_control(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as tmp:
            directory = Path(tmp)
            root = directory / "shared"
            root.mkdir()
            result = self.invoke(directory, (root, root))
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "passed")
            self.assertTrue(any("WAL" in check for check in report["passed"]))
            self.assertEqual(list(root.iterdir()), [])

    def test_unshared_directories_cannot_pass_as_shared_storage(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as tmp:
            directory = Path(tmp)
            roots = (directory / "one", directory / "two")
            for root in roots:
                root.mkdir()
            result = self.invoke(directory, roots)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["passed"], [])
            self.assertTrue(report["synthetic_data_retained"])
            self.assertEqual(list(roots[1].iterdir()), [])

    def test_nonempty_directory_is_untouched(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as tmp:
            directory = Path(tmp)
            root = directory / "shared"
            root.mkdir()
            sentinel = root / "do-not-touch"
            sentinel.write_bytes(b"existing data")
            result = self.invoke(directory, (root, root))
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn("empty, dedicated directory", json.loads(result.stdout)["error"])
            self.assertEqual(sentinel.read_bytes(), b"existing data")
            self.assertEqual(list(root.iterdir()), [sentinel])

    def test_9p_refused_before_writes(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as tmp:
            with mock.patch.object(MODULE, "filesystem", return_value="9p"):
                with self.assertRaisesRegex(RuntimeError, "9p refused"):
                    MODULE.Worker(tmp, "storage-contract-" + "a" * 32)
            self.assertEqual(list(Path(tmp).iterdir()), [])

    def test_existing_report_is_preserved_before_worker_start(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as tmp:
            directory = Path(tmp)
            root = directory / "shared"
            root.mkdir()
            report = directory / "result.json"
            report.write_text("original report")
            result = self.invoke(directory, (root, root), report)
            self.assertEqual(result.returncode, 2)
            self.assertEqual(report.read_text(), "original report")
            self.assertEqual(list(root.iterdir()), [])

    def test_stale_eof_cannot_pass_descriptor_gate(self):
        with prepared_peers() as (a, b):
            dispatch = b.worker.dispatch

            def stale_read(command):
                if command["op"] == "read" and command["handle"] == "eof":
                    return MODULE.digest(b"")
                return dispatch(command)

            with mock.patch.object(b.worker, "dispatch", side_effect=stale_read):
                with self.assertRaisesRegex(AssertionError, "peer growth/shrink"):
                    MODULE.check_persistent_eof(a, b, [])

    def test_stale_positive_lookup_cannot_pass_rename_gate(self):
        with prepared_peers() as (a, b):
            dispatch = b.worker.dispatch

            def stale_open(command):
                if command["op"] == "open" and command["handle"] == "rename-fresh":
                    return dispatch({"op": "duplicate", "handle": "rename-old",
                                     "duplicate": "rename-fresh"})
                return dispatch(command)

            with mock.patch.object(b.worker, "dispatch", side_effect=stale_open):
                with self.assertRaisesRegex(AssertionError, "replacement inode"):
                    MODULE.check_namespace(a, b, [])

    def test_disconnected_read_cache_cannot_pass_mixed_io_gate(self):
        with prepared_peers() as (a, b):
            a.call("open", name="mapping", handle="mapping", create=True)
            a.call("size", handle="mapping", size=8192)
            b.call("open", name="mapping", handle="mapping")
            for peer in (a, b):
                peer.call("map")
            dispatch = b.worker.dispatch

            def stale_read(command):
                if command["op"] == "read" and command["handle"] == "mapping":
                    return MODULE.digest(bytes(8192))
                return dispatch(command)

            with mock.patch.object(b.worker, "dispatch", side_effect=stale_read):
                with self.assertRaisesRegex(AssertionError, "peer mmap store"):
                    MODULE.check_mixed_io(a, b, bytearray(8192), [])


if __name__ == "__main__":
    unittest.main()
