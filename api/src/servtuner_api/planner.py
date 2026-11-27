"""The capacity planner behind POST /plans.

All planning maths stays in Go: this module fetches each run's
capacity_model.json from the bundle store, runs ``servtuner plan`` on them, and
returns plan.json. It is pure computation, so it runs synchronously.
"""

import json
import subprocess
import tempfile
from pathlib import Path

from servtuner_api.bundles import BundleStore
from servtuner_api.cli_contract import EXIT_INVALID, error_message

CAPACITY_MODEL_FILE = "capacity_model.json"
PLAN_TIMEOUT_SECONDS = 30.0


class PlannerUnavailableError(RuntimeError):
    """The api has no bench binary to plan with."""


class MissingCapacityModelError(LookupError):
    """A run has no capacity_model.json (not analyzed, or analyzed by an older bench)."""


class PlanError(ValueError):
    """bench refused the request or could not plan it; the message says why."""

    def __init__(self, message: str, *, is_invalid_input: bool) -> None:
        super().__init__(message)
        self.is_invalid_input = is_invalid_input


class Planner:
    def __init__(self, bench_binary: Path | None, store: BundleStore) -> None:
        self._bench_binary = bench_binary
        self._store = store

    def plan(
        self,
        request_document: object,
        bundle_keys: list[str],
        payload_keys: list[str] | None = None,
    ) -> object:
        """Plan the runs whose bundles are at bundle_keys, with a payload series at payload_keys."""
        if self._bench_binary is None:
            raise PlannerUnavailableError(
                "SERVTUNER_BENCH_BINARY is not set: there is no bench to plan with"
            )
        models = self._capacity_models(bundle_keys)
        series = self._capacity_models(payload_keys or [])
        with tempfile.TemporaryDirectory(prefix="servtuner-plan-") as directory:
            root = Path(directory)
            request_path = root / "plan-request.json"
            request_path.write_text(json.dumps(request_document))
            command = [str(self._bench_binary), "plan", "--input", str(request_path)]
            command += ["--models", ",".join(_write_all(root, "model", models))]
            if series:
                command += ["--payload-models", ",".join(_write_all(root, "payload", series))]
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                command,
                capture_output=True,
                text=True,
                timeout=PLAN_TIMEOUT_SECONDS,
                check=False,
            )
        if completed.returncode != 0:
            message = (
                completed.stderr.strip().splitlines()[-1]
                if completed.stderr.strip()
                else "servtuner plan failed"
            )
            raise PlanError(
                error_message(message),
                is_invalid_input=completed.returncode == EXIT_INVALID,
            )
        result: object = json.loads(completed.stdout)
        return result

    def _capacity_models(self, bundle_keys: list[str]) -> list[bytes]:
        models = []
        for key in bundle_keys:
            content = self._store.get(key, CAPACITY_MODEL_FILE)
            if content is None:
                raise MissingCapacityModelError(f"bundle {key} has no {CAPACITY_MODEL_FILE}")
            models.append(content)
        return models


def _write_all(root: Path, prefix: str, contents: list[bytes]) -> list[str]:
    paths = []
    for index, content in enumerate(contents):
        path = root / f"{prefix}-{index}.json"
        path.write_bytes(content)
        paths.append(str(path))
    return paths
