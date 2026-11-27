"""``servtuner propose``: the next configuration to try, from a session's evidence."""

from __future__ import annotations

import json
import subprocess
import tempfile
from collections.abc import Mapping
from pathlib import Path
from typing import Any

from servtuner_api.bundles import BundleStore
from servtuner_api.cli_contract import EXIT_INVALID, error_message
from servtuner_api.comparer import OPTIONAL_FILES, REQUIRED_FILES, stage_bundle

PRESET_REFERENCE = r"^[a-z][a-z0-9-]*@[1-9][0-9]*$"


PROPOSE_TIMEOUT_SECONDS = 60.0


# servtuner propose reads what servtuner compare reads, plus each run's advice.
PROPOSE_FILES = (*REQUIRED_FILES, "recommendations.json")


class ProposeError(RuntimeError):
    """servtuner propose refused its inputs or failed."""

    def __init__(self, message: str, *, is_invalid_input: bool) -> None:
        super().__init__(message)
        self.is_invalid_input = is_invalid_input


class Proposer:
    """Runs ``servtuner propose`` over staged bundles, the space and the boot results."""

    def __init__(self, bench_binary: Path | None, store: BundleStore) -> None:
        self._bench_binary = bench_binary
        self._store = store

    def propose(
        self, space: Mapping[str, Any], bundle_keys: list[str], boot_results: list[dict[str, Any]]
    ) -> dict[str, Any]:
        if self._bench_binary is None:
            raise ProposeError(
                "SERVTUNER_BENCH_BINARY is not set: no bench to propose with",
                is_invalid_input=False,
            )
        with tempfile.TemporaryDirectory(prefix="servtuner-propose-") as directory:
            root = Path(directory)
            (root / "space.json").write_text(json.dumps(dict(space)))
            (root / "boots.json").write_text(json.dumps({"results": boot_results}))
            command = [
                str(self._bench_binary),
                "propose",
                "--space",
                str(root / "space.json"),
                "--infeasible",
                str(root / "boots.json"),
            ]
            staged = [
                stage_bundle(
                    self._store, key, root / "bundles" / str(i), PROPOSE_FILES, OPTIONAL_FILES
                )
                for i, key in enumerate(bundle_keys)
            ]
            if staged:
                command += ["--bundles", ",".join(staged)]
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                command,
                capture_output=True,
                text=True,
                timeout=PROPOSE_TIMEOUT_SECONDS,
                check=False,
            )
        if completed.returncode != 0:
            lines = completed.stderr.strip().splitlines() or ["servtuner propose failed"]
            raise ProposeError(
                error_message(lines[-1]), is_invalid_input=completed.returncode == EXIT_INVALID
            )
        proposal: dict[str, Any] = json.loads(completed.stdout)
        return proposal
