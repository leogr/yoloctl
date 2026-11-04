"""Check that the large-write oracle distinguishes interleaving from data loss."""
import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("followup", Path(__file__).with_name("followup.py"))
FOLLOWUP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FOLLOWUP)


class AppendOracleTest(unittest.TestCase):
    def test_whole_syscalls_in_either_order(self):
        self.assertEqual(FOLLOWUP.append_order(b"AAAABBBB", b"AAAA", b"BBBB"), "a-then-b")
        self.assertEqual(FOLLOWUP.append_order(b"BBBBAAAA", b"AAAA", b"BBBB"), "b-then-a")

    def test_fragmented_complete_writes_are_a_semantic_failure(self):
        self.assertEqual(FOLLOWUP.append_order(b"AABBAABB", b"AAAA", b"BBBB"), "interleaved")

    def test_missing_or_changed_bytes_are_not_just_interleaving(self):
        for invalid in (b"AABB", b"AAAAAAAB", b"AAAACBBB"):
            with self.assertRaises(AssertionError):
                FOLLOWUP.append_order(invalid, b"AAAA", b"BBBB")


if __name__ == "__main__":
    unittest.main()
