"""The experiment comparison behind POST /experiments/{name}/analyze.

All comparison maths stays in Go: this module copies the files ``bench
compare`` reads from each run's bundle into a scratch directory, whatever the
store, runs it, and returns comparison.json.
"""

import json
import subprocess
import tempfile
from pathlib import Path

from servtuner_api.bundles import BundleStore
from servtuner_api.cli_contract import EXIT_INVALID, error_message

# The files servtuner compare reads (analyze.LoadComparisonRun). The first four are
# small; requests.parquet is streamed, and only latency objectives read it.
REQUIRED_FILES = ("manifest.json", "spec.resolved.json", "analysis.json")
OPTIONAL_FILES = ("capacity_model.json", "requests.parquet")
# The pooled bootstrap reads every request of a stage, so allow for large runs.
COMPARE_TIMEOUT_SECONDS = 300.0


class ComparerUnavailableError(RuntimeError):
    """The api has no bench binary to compare with."""


class MissingBundleFileError(LookupError):
    """A run's bundle lacks a file servtuner compare needs."""


class CompareError(ValueError):
    """bench refused the runs or could not compare them; the message says why."""

    def __init__(self, message: str, *, is_invalid_input: bool) -> None:
        super().__init__(message)
        self.is_invalid_input = is_invalid_input


class Comparer:
    def __init__(self, bench_binary: Path | None, store: BundleStore) -> None:
        self._bench_binary = bench_binary
        self._store = store

    def compare(self, bundles: dict[str, str], objective: str | None) -> object:
        """Compare the runs, given as run id -> bundle key; return comparison.json."""
        if self._bench_binary is None:
            raise ComparerUnavailableError(
                "SERVTUNER_BENCH_BINARY is not set: there is no bench to compare with"
            )
        with tempfile.TemporaryDirectory(prefix="servtuner-compare-") as directory:
            root = Path(directory)
            directories = [self._stage(root / run_id, key) for run_id, key in bundles.items()]
            command = [str(self._bench_binary), "compare", "--bundles", ",".join(directories)]
            if objective is not None:
                command += ["--objective", objective]
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                command,
                capture_output=True,
                text=True,
                timeout=COMPARE_TIMEOUT_SECONDS,
                check=False,
            )
        if completed.returncode != 0:
            lines = completed.stderr.strip().splitlines()
            message = lines[-1] if lines else "servtuner compare failed"
            raise CompareError(
                error_message(message),
                is_invalid_input=completed.returncode == EXIT_INVALID,
            )
        result: object = json.loads(completed.stdout)
        return result

    def _stage(self, directory: Path, key: str) -> str:
        return stage_bundle(self._store, key, directory, REQUIRED_FILES, OPTIONAL_FILES)


def stage_bundle(
    store: BundleStore,
    key: str,
    directory: Path,
    required: tuple[str, ...],
    optional: tuple[str, ...] = (),
) -> str:
    """Copy a bundle's files from any store into a scratch directory for a bench subcommand."""
    directory.mkdir(parents=True)
    for name in required + optional:
        chunks = store.stream(key, name)
        if chunks is None:
            if name in required:
                raise MissingBundleFileError(f"bundle {key} has no {name}")
            continue
        with (directory / name).open("wb") as file:
            for chunk in chunks:
                file.write(chunk)
    return str(directory)
