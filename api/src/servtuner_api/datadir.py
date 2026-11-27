"""The data directory and the layout under it.

``resolve_data_dir`` is the api's copy of bench's rule: ``$SERVTUNER_DATA_DIR``,
else ``$XDG_DATA_HOME/servtuner``, else ``~/.local/share/servtuner``. Everything
that derives a path from a run id lives here so the layout has one definition.
"""

from collections.abc import Mapping
from pathlib import Path, PurePath

DATA_DIR_ENV = "SERVTUNER_DATA_DIR"
XDG_DATA_HOME_ENV = "XDG_DATA_HOME"
DATA_DIR_NAME = "servtuner"
RUNS_DIR_NAME = "runs"
SPECS_DIR_NAME = "specs"
TOKENS_DIR_NAME = "tokens"
LOGS_DIR_NAME = "logs"


def resolve_data_dir(env: Mapping[str, str], home: Path | None = None) -> Path:
    if env.get(DATA_DIR_ENV):
        return Path(env[DATA_DIR_ENV])
    if env.get(XDG_DATA_HOME_ENV):
        return Path(env[XDG_DATA_HOME_ENV]) / DATA_DIR_NAME
    return (home or Path.home()) / ".local" / "share" / DATA_DIR_NAME


def run_bundle_dir[DataDir: PurePath](data_dir: DataDir, run_id: str) -> DataDir:
    """Works on a local Path and on the PurePosixPath of a Job's mount alike."""
    return data_dir / RUNS_DIR_NAME / run_id
