"""Safety and handoff checks for the fallback runner; no VMs or real accounts."""

import importlib.util
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "single_kernel_account", Path(__file__).with_name("single-kernel-account.py"))
experiment = importlib.util.module_from_spec(spec)
spec.loader.exec_module(experiment)


class HandoffTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp(prefix="yoloctl-handoff-test-", dir="/tmp"))
        self.addCleanup(shutil.rmtree, self.root)
        self.data = self.root / "handoff"
        self.data.mkdir()
        for name, data in experiment.OLD_FILES.items():
            (self.data / name).write_bytes(data)

    def test_preserved_timestamp_and_replaced_names(self):
        old_stat = (self.data / "same-size").stat()
        experiment.host_edit(self.data, self.root)
        self.assertEqual(experiment.contents(self.data), experiment.NEW_FILES)
        new_stat = (self.data / "same-size").stat()
        self.assertEqual(old_stat.st_size, new_stat.st_size)
        self.assertEqual(old_stat.st_mtime_ns, new_stat.st_mtime_ns)

    def test_rejects_outside_root_and_symlink(self):
        with self.assertRaises(ValueError):
            experiment.host_edit(self.data, self.root / "different-root")
        linked = self.root / "linked"
        linked.symlink_to(self.data)
        with self.assertRaises(ValueError):
            experiment.host_edit(linked, self.root)
        self.assertEqual(experiment.contents(self.data), experiment.OLD_FILES)

    def test_changed_or_symlinked_input_is_never_overwritten(self):
        (self.data / "replace").write_bytes(b"unexpected")
        with self.assertRaises(RuntimeError):
            experiment.host_edit(self.data, self.root)
        self.assertEqual((self.data / "same-size").read_bytes(), b"before01")
        (self.data / "replace").unlink()
        (self.data / "replace").symlink_to(self.data / "same-size")
        with self.assertRaises(OSError):
            experiment.host_edit(self.data, self.root)
        self.assertEqual((self.data / "same-size").read_bytes(), b"before01")

    def test_stop_failure_prevents_host_edits_and_restart(self):
        trial = experiment.Trial("minimal-26.04")
        self.addCleanup(shutil.rmtree, trial.root)

        def cli(label, *_args, **_kwargs):
            if label == "handoff-stop-" + experiment.BOXES[1]:
                raise RuntimeError("second box could not stop")
            return 0

        with patch.object(trial, "cli", side_effect=cli) as commands, \
                patch.object(experiment, "host_edit") as edit:
            with self.assertRaises(RuntimeError):
                trial.handoff(self.root, "/home/sandbox/.codex")
        edit.assert_not_called()
        self.assertFalse(any(call.args[0].startswith("handoff-start-") for call in commands.call_args_list))

    def test_overlay_preserves_provisioning_and_mount_grants(self):
        source = (experiment.guest_image.REPO / "assets/lima/yoloctl.yaml.tmpl").read_text()
        result = experiment.trial_template(source, "minimal-26.04")
        self.assertEqual(source[source.index("mounts:"):], result[result.index("mounts:"):])
        self.assertIn('vmType: "qemu"\nmountType: "virtiofs"', result)
        self.assertNotIn('mountType: "9p"', result)


if __name__ == "__main__":
    unittest.main()
