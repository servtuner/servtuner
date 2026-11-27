"""Polling helper: the run watcher is a background thread, so tests wait for it."""

import time
from collections.abc import Callable

WAIT_TIMEOUT_SECONDS = 10.0
WAIT_INTERVAL_SECONDS = 0.02


def wait_until(condition: Callable[[], bool], description: str) -> None:
    deadline = time.monotonic() + WAIT_TIMEOUT_SECONDS
    while not condition():
        if time.monotonic() > deadline:
            raise AssertionError(f"timed out waiting for {description}")
        time.sleep(WAIT_INTERVAL_SECONDS)
