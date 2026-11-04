"""Regression controls for exact ELF interpreter paths in minimal roots."""
import importlib.util
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("trial", Path(__file__).with_name("trial.py"))
TRIAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(TRIAL)


def executable(path, interpreter=None):
    header = bytearray(64)
    header[:7] = b"\x7fELF\x02\x01\x01"
    struct.pack_into("<Q", header, 32, 64)
    struct.pack_into("<HH", header, 54, 56, 1)
    program = bytearray(56)
    tail = b"" if interpreter is None else interpreter.encode() + b"\0"
    struct.pack_into("<I", program, 0, 1 if interpreter is None else 3)
    struct.pack_into("<Q", program, 8, 120)
    struct.pack_into("<Q", program, 32, len(tail))
    path.write_bytes(header + program + tail)


class PackagingTest(unittest.TestCase):
    def test_merged_usr_ldd_output_preserves_execve_interpreter_path(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            root = Path(directory)
            binary = root / "program"
            executable(binary, "/lib64/ld-linux-x86-64.so.2")
            result = subprocess.CompletedProcess([], 0,
                "\t/lib64/ld-linux-x86-64.so.2 => /usr/lib64/ld-linux-x86-64.so.2 (0x7ff)\n", "")
            with mock.patch.object(TRIAL.subprocess, "run", return_value=result), \
                    mock.patch.object(TRIAL, "copy_file") as copy:
                TRIAL.dependencies(binary, root / "package")
            self.assertIn(mock.call(Path("/lib64/ld-linux-x86-64.so.2"), root / "package"), copy.call_args_list)
            self.assertIn(mock.call(Path("/usr/lib64/ld-linux-x86-64.so.2"), root / "package"), copy.call_args_list)

    def test_static_object_has_no_interpreter(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            binary = Path(directory) / "program"
            executable(binary)
            self.assertIsNone(TRIAL.elf_interpreter(binary))

    def test_invalid_interpreter_path_is_rejected(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            binary = Path(directory) / "program"
            for invalid in ("relative/loader", "/../loader"):
                executable(binary, invalid)
                with self.assertRaisesRegex(ValueError, "absolute path"):
                    TRIAL.elf_interpreter(binary)

    def test_truncated_header_is_rejected(self):
        with tempfile.TemporaryDirectory(dir="/tmp") as directory:
            binary = Path(directory) / "program"
            binary.write_bytes(b"\x7fELF")
            with self.assertRaisesRegex(ValueError, "ELF64"):
                TRIAL.elf_interpreter(binary)


if __name__ == "__main__":
    unittest.main()
