"""Shared helpers for running tool4d."""

from __future__ import annotations

from pathlib import Path
import signal
import subprocess
import time

VERSION_ATTEMPTS = 3
RETRY_DELAY = 1.0


def version(tool: Path, timeout: float) -> str:
    """Return `tool4d --version`, retrying only when it crashes with SIGSEGV.

    On the GitHub-hosted macos-15-arm64 image, `tool4d --version` intermittently dies with SIGSEGV before
    doing anything (sonar-4d#7, #9, #10); a rerun passes. Any other failure, or a crash that persists,
    is raised as before.
    """
    for attempt in range(1, VERSION_ATTEMPTS + 1):
        try:
            return subprocess.run(
                [str(tool), "--version"], capture_output=True, text=True, timeout=timeout, check=True,
            ).stdout.strip()
        except subprocess.CalledProcessError as error:
            if error.returncode != -signal.SIGSEGV or attempt == VERSION_ATTEMPTS:
                raise
            time.sleep(RETRY_DELAY)
    raise AssertionError("unreachable")
