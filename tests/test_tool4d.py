from pathlib import Path
import subprocess
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from sonar4d import tool4d


TOOL = Path("/opt/tool4d.app/Contents/MacOS/tool4d")


def crash(code=-11):
    return subprocess.CalledProcessError(code, [str(TOOL), "--version"])


class VersionTest(unittest.TestCase):
    def run_version(self, outcomes):
        with patch.object(tool4d.subprocess, "run", side_effect=outcomes) as run, \
                patch.object(tool4d.time, "sleep") as sleep:
            try:
                return tool4d.version(TOOL, 15), run, sleep
            except Exception as error:  # noqa: BLE001 - returned for assertions
                return error, run, sleep

    def test_segfault_is_retried_until_version_succeeds(self):
        ok = SimpleNamespace(stdout="21.0 build 21.100422\n", stderr="")
        result, run, sleep = self.run_version([crash(), crash(), ok])
        self.assertEqual(result, "21.0 build 21.100422")
        self.assertEqual(run.call_count, 3)
        self.assertEqual(sleep.call_count, 2)
        self.assertEqual(run.call_args.args[0], [str(TOOL), "--version"])
        self.assertEqual(run.call_args.kwargs["timeout"], 15)
        self.assertTrue(run.call_args.kwargs["check"])

    def test_persistent_segfault_is_raised_after_the_last_attempt(self):
        result, run, _ = self.run_version([crash()] * tool4d.VERSION_ATTEMPTS)
        self.assertIsInstance(result, subprocess.CalledProcessError)
        self.assertEqual(result.returncode, -11)
        self.assertEqual(run.call_count, tool4d.VERSION_ATTEMPTS)

    def test_other_failures_are_not_retried(self):
        for error in (crash(1), crash(-6), subprocess.TimeoutExpired([str(TOOL)], 15), FileNotFoundError(str(TOOL))):
            with self.subTest(error=type(error).__name__):
                result, run, sleep = self.run_version([error])
                self.assertIs(result, error)
                self.assertEqual(run.call_count, 1)
                sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
