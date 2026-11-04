"""Local checks for the experiment's isolation and failure handling; no VMs."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("guest_image", Path(__file__).with_name("guest-image.py"))
guest_image = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guest_image)


class TemplateTest(unittest.TestCase):
    def test_only_image_integration_and_transport_change(self):
        source = (guest_image.REPO / "assets/lima/yoloctl.yaml.tmpl").read_text()
        candidate = guest_image.trial_template(source, "minimal-26.04")
        baseline = guest_image.trial_template(source, "ubuntu-24.04")
        for template in (candidate, baseline):
            self.assertEqual(source[source.index("mounts:"):], template[template.index("mounts:"):])
            self.assertIn('vmType: "qemu"\nmountType: "9p"', template)
        self.assertNotIn("template:_images/", candidate)
        self.assertEqual(candidate.count("  digest: sha256:"), 2)
        self.assertIn("internal_netplanOptional", candidate)
        self.assertIn("template:_images/ubuntu-24.04", baseline)

    def test_changed_shipped_image_requires_review(self):
        with self.assertRaises(ValueError):
            guest_image.trial_template("base:\n- template:_images/ubuntu-28.04\n", "minimal-26.04")


class RunnerTest(unittest.TestCase):
    def setUp(self):
        self.trial = guest_image.Trial("minimal-26.04")
        self.addCleanup(shutil.rmtree, self.trial.root)

    def test_inherited_lima_and_smoke_roots_are_not_used(self):
        with patch.dict(os.environ, {"LIMA_HOME": "/do-not-touch", "LIMA_CACHE_HOME": "/do-not-touch",
                                     "SMOKE_SPACE": "production", "SMOKE_PRESERVE_SPACE": "1",
                                     "SMOKE_ROOT": "/do-not-touch"}):
            other = guest_image.Trial("minimal-26.04")
        self.addCleanup(shutil.rmtree, other.root)
        self.assertNotIn("LIMA_HOME", other.env)
        self.assertNotIn("SMOKE_PRESERVE_SPACE", other.env)
        self.assertEqual(other.env["SMOKE_SPACE"], "gi")
        for key in ("LIMA_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "SMOKE_ROOT", "SMOKE_WORKDIR"):
            self.assertTrue(Path(other.env[key]).is_relative_to(other.root))

    def test_timeout_is_bounded_and_recorded(self):
        with self.assertRaises(subprocess.TimeoutExpired):
            self.trial.run("timeout-test", [sys.executable, "-c", "import time; time.sleep(60)"], timeout=0.1)
        report = json.loads((self.trial.root / "report.json").read_text())
        self.assertTrue(report["steps"][-1]["interrupted"])
        self.assertLess(report["steps"][-1]["seconds"], 5)

    def test_cleanup_continues_and_preserves_artifacts_on_failure(self):
        def cli(label, *args, **_kwargs):
            if label.startswith("cleanup-list"):
                (self.trial.root / f"{label}.log").write_text('[{"name":"test-box"}]')
            if label == "cleanup-gi-b":
                raise RuntimeError("VM delete failed")

        self.trial.started = True
        with patch.object(self.trial, "cli", side_effect=cli) as mocked:
            self.trial.cleanup()
        labels = [call.args[0] for call in mocked.call_args_list]
        self.assertLess(labels.index("cleanup-box-gi-test-box"), labels.index("cleanup-gi"))
        self.assertIn("cleanup-gi-b", labels)
        self.assertIn("cleanup-gi", labels)
        self.assertEqual(self.trial.report["status"], "cleanup-failed")
        self.assertTrue(self.trial.root.is_dir())

    def test_append_mismatch_still_runs_lifecycle_phases(self):
        config = self.trial.root / "resolved-source.yaml"
        config.write_text("test config")

        def cli(label, *_args, **_kwargs):
            data = None
            if label == "space-show":
                data = {"config_path": str(config), "dir": str(config.parent)}
            elif label == "account-show":
                data = {"state_home": str(self.trial.root / "account")}
            elif label == "probe-box-show":
                data = {"workdir": {"container_path": "/workdir/test"},
                        "account_state": {"destination": "/home/sandbox/.codex"}}
            if data is not None:
                (self.trial.root / f"{label}.log").write_text(json.dumps(data))
            return 1 if label == "append-integrity" else 0

        with patch.object(self.trial, "cli", side_effect=cli), \
                patch.object(self.trial, "run", return_value=0) as run, \
                patch.object(self.trial, "port_probe"):
            self.trial.experiment()
        self.assertFalse(self.trial.report["storage_pass"])
        self.assertIn("smoke-box-attach", [call.args[0] for call in run.call_args_list])


if __name__ == "__main__":
    unittest.main()
