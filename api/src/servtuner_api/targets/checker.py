"""Checking a target with bench: its keys reach bench as one-time files."""

import json
import os
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from servtuner_api.cli_contract import EXIT_INVALID, error_message
from servtuner_api.ids import new_ulid
from servtuner_api.resources.targets import (
    TargetCheckView,
)
from servtuner_api.secret_store import (
    SecretStore,
    materialize,
    remove_materialized,
    secret_names_in,
)
from servtuner_api.targets.common import (
    CHECK_TIMEOUT_SECONDS,
    ONE_TIME_SECRET,
    TOKEN_KEY,
    CheckUnavailableError,
    TargetInputError,
)


@dataclass(frozen=True)
class TargetChecker:
    """Runs ``servtuner check-target`` with a private secrets directory."""

    bench_binary: Path | None
    run_secrets_dir: Path | None
    store: SecretStore | None
    mounted_dir: Path | None
    checked_from: str

    def check(
        self, request: dict[str, Any], one_time: tuple[str, str] | None = None
    ) -> TargetCheckView:
        """Check ``request`` (a check-target-request); ``one_time`` is (secret name, key value)."""
        stdout = self._bench("check-target", request, one_time)
        return TargetCheckView.model_validate(
            {"result": json.loads(stdout), "checked_from": self.checked_from}
        )

    def boot_probe(self, request: dict[str, Any]) -> dict[str, Any]:
        """``servtuner boot-probe`` on a booted managed target: a tuningBootResult."""
        result: dict[str, Any] = json.loads(self._bench("boot-probe", request, None))
        return result

    def _bench(
        self, subcommand: str, request: dict[str, Any], one_time: tuple[str, str] | None
    ) -> str:
        """Run a bench subcommand that reads ``--input`` and the request's Secrets."""
        if self.bench_binary is None:
            raise CheckUnavailableError(
                f"SERVTUNER_BENCH_BINARY is not set: no bench to {subcommand}"
            )
        root = self.run_secrets_dir or Path(tempfile.gettempdir())
        root.mkdir(mode=0o700, parents=True, exist_ok=True)
        secrets_dir = root / f"check-{new_ulid()}"
        try:
            names = [name for name in secret_names_in(request) if name != ONE_TIME_SECRET]
            materialize(names, secrets_dir, self.store, self.mounted_dir)
            if one_time is not None:
                _write_one_time(secrets_dir, one_time[1])
            request_path = secrets_dir / f"{subcommand}-request.json"
            request_path.write_text(json.dumps(request))
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                [
                    str(self.bench_binary),
                    subcommand,
                    "--input",
                    str(request_path),
                    "--secrets-dir",
                    str(secrets_dir),
                ],
                capture_output=True,
                text=True,
                timeout=CHECK_TIMEOUT_SECONDS,
                check=False,
            )
        except subprocess.TimeoutExpired:
            raise CheckUnavailableError(
                f"bench {subcommand} did not finish in {CHECK_TIMEOUT_SECONDS:.0f} s"
            ) from None
        finally:
            remove_materialized(secrets_dir)
        if completed.returncode != 0:
            last = completed.stderr.strip().splitlines()[-1] if completed.stderr.strip() else ""
            message = error_message(last) or f"servtuner {subcommand} failed"
            if completed.returncode == EXIT_INVALID:
                raise TargetInputError([message])
            raise CheckUnavailableError(message)
        return completed.stdout


def _write_one_time(secrets_dir: Path, value: str) -> None:
    directory = secrets_dir / ONE_TIME_SECRET
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor = os.open(directory / TOKEN_KEY, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o400)
    with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
        handle.write(value)
