"""Maintenance boundaries; these tests require neither Rust nor downloads."""

import importlib.util
import json
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


builder = load("mermaid_build", ROOT / "scripts/build-mermaid.py")
normalizer = load("mermaid_rustc", ROOT / "third_party/mermaid-text/rustc-repro.py")


class MermaidMaintenanceTest(unittest.TestCase):
    def test_relocated_crates_keep_identity_but_different_packages_do_not(self):
        env = {"YOLOCTL_MERMAID_RECIPE_HASH": "recipe", "CARGO_PKG_NAME": "mermaid-text",
               "CARGO_PKG_VERSION": "0.58.0"}
        def args(path, old):
            return ["--crate-name", "mermaid_text", path + "/lib.rs", "--crate-type", "lib",
                    "--target", "wasm32-wasip1", "-C", "metadata=" + old,
                    "--remap-path-prefix=" + path + "=/source"]
        def identity(arguments, environ):
            result = normalizer.normalized_args(arguments, environ)
            return result[result.index("-C") + 1]
        expected = identity(args("/first", "one"), env)
        self.assertEqual(expected, identity(args("/second", "two"), env))
        for key, value in [("CARGO_PKG_VERSION", "0.59.0"),
                           ("CARGO_PKG_NAME", "different"),
                           ("YOLOCTL_MERMAID_RECIPE_HASH", "new recipe")]:
            self.assertNotEqual(expected, identity(args("/first", "one"), dict(env, **{key: value})))
        feature = args("/first", "one") + ["--cfg", 'feature="extra"']
        self.assertNotEqual(expected, identity(feature, env))

    def test_compiler_queries_are_not_rewritten(self):
        self.assertEqual(["--version"], normalizer.normalized_args(["--version"], {}))

    def test_bad_archive_is_refused_before_extraction_or_patch(self):
        pins, _ = builder.load_inputs()
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            archive = root / "source.crate"
            archive.write_bytes(b"not the pinned crate")
            with patch.object(builder.subprocess, "run") as run:
                with self.assertRaisesRegex(ValueError, "source archive hash mismatch"):
                    builder.prepare(root / "build", archive, pins)
                run.assert_not_called()
            self.assertFalse((root / "build").exists())

    def test_modified_patch_is_not_silently_accepted(self):
        with tempfile.TemporaryDirectory() as name:
            copied = Path(name) / "pins"
            shutil.copytree(builder.PINS, copied)
            pins = json.loads((copied / "pins.json").read_text())
            target = copied / pins["patches"][0]["file"]
            target.write_bytes(target.read_bytes() + b"\n")
            with patch.object(builder, "PINS", copied):
                with self.assertRaisesRegex(ValueError, "patch differs from pins"):
                    builder.load_inputs()

    def test_inherited_build_flags_cannot_select_a_different_recipe(self):
        pins, key = builder.load_inputs()
        with patch.dict(builder.os.environ, {"RUSTFLAGS": "-C opt-level=0",
                         "CARGO_ENCODED_RUSTFLAGS": "-Cpanic=unwind",
                         "CARGO_PROFILE_RELEASE_LTO": "false", "CARGO_TARGET_DIR": "/unrelated"}):
            env = builder.build_env(Path("/build root"), pins, key, "/rustc")
        self.assertNotIn("RUSTFLAGS", env)
        self.assertNotIn("CARGO_PROFILE_RELEASE_LTO", env)
        self.assertEqual(env["CARGO_TARGET_DIR"], "/build root/worker/target")
        self.assertIn("--remap-path-prefix=/build root=/yoloctl-mermaid",
                      env["CARGO_ENCODED_RUSTFLAGS"].split("\x1f"))
        self.assertEqual(env["CARGO_INCREMENTAL"], "0")


if __name__ == "__main__":
    unittest.main()
