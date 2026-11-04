import os
from pathlib import Path
import signal
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).parent))
from terminal_probe import measure


class TerminalProbeTests(unittest.TestCase):
    def test_confined_marker_is_observed_and_normal_detach_finishes(self):
        for blocked in (False, True):
            with self.subTest(inherited_sigterm_blocked=blocked), tempfile.TemporaryDirectory() as root:
                previous = signal.pthread_sigmask(
                    signal.SIG_BLOCK if blocked else signal.SIG_UNBLOCK, {signal.SIGTERM})
                try:
                    # READY proves the stop signal is blocked and can be waited
                    # for synchronously, even if it arrives before sigwait.
                    # The fixture has no interpreter shutdown work to measure.
                    program = (
                        'import os,signal\n'
                        'signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM})\n'
                        'os.write(1, b"READY_FIXTURE\\n")\n'
                        'signal.sigwait({signal.SIGTERM})\n'
                        'os._exit(0)\n'
                    )
                    result = measure([sys.executable, '-c', program], os.environ, Path(root)/'tty', b'READY_FIXTURE', 3)
                finally:
                    signal.pthread_sigmask(signal.SIG_SETMASK, previous)
                self.assertGreater(result, 0)

    def test_missing_marker_fails_and_reaps_child(self):
        with tempfile.TemporaryDirectory() as root:
            with self.assertRaises(TimeoutError):
                measure([sys.executable, '-c', 'import time; time.sleep(10)'], os.environ, Path(root)/'tty', b'NEVER', .1)
