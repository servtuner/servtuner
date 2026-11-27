"""Which servtuner build is running: stamped into the api image, never guessed at runtime."""

import os
from dataclasses import dataclass
from functools import cache

from servtuner_api.settings.env import ENV_COMMIT, ENV_VERSION

DEV_VERSION = "0.0.0-dev"  # a build that no release stamped
UNKNOWN_COMMIT = "unknown"


@dataclass(frozen=True)
class BuildInfo:
    version: str
    commit: str


@cache
def build_info() -> BuildInfo:
    """Read once: the image's environment does not change while the process runs."""
    return BuildInfo(
        version=os.environ.get(ENV_VERSION) or DEV_VERSION,
        commit=os.environ.get(ENV_COMMIT) or UNKNOWN_COMMIT,
    )
