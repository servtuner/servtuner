"""Runners launch ``servtuner run`` and report its final state (api/CLAUDE.md).

The contract with bench is a CLI plus files, never Kubernetes (root
CLAUDE.md rule 1):

    servtuner run --spec <path> --out <bundle dir> --run-id <ULID> [--engine-override null]
              [--api-url <url> --run-token-file <path> --api-tls true|false
               [--tls-cert-file <path> --tls-key-file <path>]]

A runner owns everything its environment needs to hand bench a spec and a
token (files for the subprocess runner; a ConfigMap and Secret later for
Kubernetes). The runner's final status is authoritative: the process exit
code decides ``succeeded`` or ``failed``, whatever bench managed to report.
"""

import json
import os
import subprocess
from collections.abc import Iterator
from dataclasses import dataclass
from enum import StrEnum
from pathlib import Path
from typing import Protocol

from servtuner_api.datadir import (
    DATA_DIR_ENV,
    LOGS_DIR_NAME,
    SPECS_DIR_NAME,
    TOKENS_DIR_NAME,
    run_bundle_dir,
)
from servtuner_api.secret_store import (
    SecretStore,
    materialize,
    remove_materialized,
    secret_names_in,
)
from servtuner_api.settings.models import (
    MOUNTED_SECRETS_DIR,
    Hop,
    HopTls,
    S3Settings,
    Settings,
)

PRIVATE_FILE_MODE = 0o600
CANCEL_GRACE_SECONDS = 10.0


class RunnerState(StrEnum):
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"


SHARD_INDEX_FLAG = "--shard-index"


def shard_count(spec_raw: bytes) -> int:
    """engine.shards of a validated RunSpec: how many bench processes share its load."""
    return shards_in(json.loads(spec_raw))


def shards_in(document: object) -> int:
    """engine.shards of a parsed RunSpec, 1 when absent."""
    engine = document.get("engine") if isinstance(document, dict) else None
    shards = engine.get("shards", 1) if isinstance(engine, dict) else 1
    return shards if isinstance(shards, int) and shards > 1 else 1


@dataclass(frozen=True)
class LaunchRequest:
    """What the api hands a runner: the spec exactly as submitted, and the run token."""

    run_id: str
    spec_raw: bytes
    run_token: str


@dataclass(frozen=True)
class RunHandle:
    run_id: str
    reference: str


@dataclass(frozen=True)
class RunnerStatus:
    state: RunnerState
    exit_code: int | None = None
    message: str | None = None

    @property
    def is_final(self) -> bool:
        return self.state is not RunnerState.RUNNING


class Runner(Protocol):
    def launch(self, request: LaunchRequest) -> RunHandle: ...

    def status(self, handle: RunHandle) -> RunnerStatus:
        """The authoritative state; a final state is never revised."""
        ...

    def cancel(self, handle: RunHandle) -> None: ...

    def logs(self, handle: RunHandle) -> Iterator[str]: ...


@dataclass(frozen=True)
class ObjectStoreClient:
    """How bench reaches the object store (the object-store hop)."""

    hop: HopTls
    cert_file: str | None = None
    key_file: str | None = None
    ca_file: str | None = None


def output_arguments(local_dir: str, s3: S3Settings | None, client: ObjectStoreClient) -> list[str]:
    """bench's --out: the bundle directory, or the s3:// location with the hop's TLS."""
    if s3 is None:
        return ["--out", local_dir]
    arguments = [
        "--out",
        f"s3://{s3.bucket}/{s3.prefix}",
        "--object-store-tls",
        tls_flag(client.hop.enabled),
    ]
    if client.hop.enabled and s3.endpoint is not None:
        flags = [("--object-store-ca-file", client.ca_file)]
        if client.hop.client_cert:
            flags = [
                ("--object-store-cert-file", client.cert_file),
                ("--object-store-key-file", client.key_file),
                *flags,
            ]
        for flag, value in flags:
            if value is not None:
                arguments += [flag, value]
    return arguments


def tls_flag(enabled: bool) -> str:
    """bench's --api-tls and --object-store-tls take true or false."""
    return "true" if enabled else "false"


@dataclass(frozen=True)
class SubprocessRunnerConfig:
    bench_binary: Path
    data_dir: Path
    api_url: str
    api_hop: HopTls
    tls_cert_file: Path | None
    tls_key_file: Path | None
    engine_override: str | None = None  # dev and e2e only (SERVTUNER_ENGINE_OVERRIDE)
    s3: S3Settings | None = None
    object_store_hop: HopTls = HopTls(enabled=False, client_cert=False)
    # Where each run's Secrets are written for bench --secrets-dir.
    run_secrets_dir: Path | None = None
    mounted_secrets_dir: Path = MOUNTED_SECRETS_DIR
    offline: bool = False  # servtuner run --offline

    @classmethod
    def from_settings(cls, settings: Settings) -> SubprocessRunnerConfig:
        assert settings.bench_binary is not None  # Settings requires it for this runner
        return cls(
            bench_binary=settings.bench_binary,
            data_dir=settings.data_dir,
            api_url=settings.api_url,
            api_hop=settings.tls.hop(Hop.BENCH_API),
            tls_cert_file=settings.tls.cert_file,
            tls_key_file=settings.tls.key_file,
            engine_override=settings.engine_override,
            offline=settings.offline,
            s3=settings.s3,
            object_store_hop=settings.tls.hop(Hop.OBJECT_STORE),
            run_secrets_dir=settings.secret_store.run_secrets_dir,
            mounted_secrets_dir=settings.secret_store.mounted_dir,
        )


class SubprocessRunner:
    """Runs bench as a local child process. For development, single-node installs and CI.

    Handles live in memory: after an api restart the runner no longer knows the
    child, which ended with the old process, and reports it ``failed``; the service
    re-watches unfinished runs at startup so they get that final state.
    """

    def __init__(
        self, config: SubprocessRunnerConfig, secret_store: SecretStore | None = None
    ) -> None:
        self._config = config
        self._secret_store = secret_store
        self._processes: dict[str, subprocess.Popen[bytes]] = {}
        # The follower shards of a sharded run (engine.shards); the leader's exit decides.
        self._followers: dict[str, list[subprocess.Popen[bytes]]] = {}
        self._cancelled: set[str] = set()

    def launch(self, request: LaunchRequest) -> RunHandle:
        spec_path = self._write_private(SPECS_DIR_NAME, request.run_id, request.spec_raw)
        token_path = self._write_private(
            TOKENS_DIR_NAME, request.run_id, request.run_token.encode()
        )
        run_bundle_dir(self._config.data_dir, request.run_id).parent.mkdir(
            parents=True, exist_ok=True
        )
        secrets_dir = self._materialize_secrets(request)
        command = self.build_command(request.run_id, spec_path, token_path, secrets_dir)
        processes = [
            self._start(
                request.run_id, [*command, SHARD_INDEX_FLAG, str(index)] if index else command
            )
            for index in range(shard_count(request.spec_raw))
        ]
        self._processes[request.run_id] = processes[0]
        self._followers[request.run_id] = processes[1:]
        return RunHandle(run_id=request.run_id, reference=str(processes[0].pid))

    def _start(self, run_id: str, command: list[str]) -> subprocess.Popen[bytes]:
        with self._log_path(run_id).open("ab") as log_file:
            return subprocess.Popen(  # noqa: S603 - the binary path is operator configuration
                command,
                stdout=log_file,
                stderr=subprocess.STDOUT,
                stdin=subprocess.DEVNULL,
                env={**os.environ, DATA_DIR_ENV: str(self._config.data_dir)},
            )

    def _materialize_secrets(self, request: LaunchRequest) -> Path | None:
        """Write the Secrets the RunSpec names where only this run's bench reads them.

        From the secret store, else from the Secrets the chart mounts
        (subprocess.secrets). A name found in neither is left for bench to report
        at resolve ("secret unavailable"), as with the Kubernetes runner's check.
        """
        names = secret_names_in(json.loads(request.spec_raw))
        if not names or self._config.run_secrets_dir is None:
            return None
        directory = self._config.run_secrets_dir / request.run_id
        materialize(names, directory, self._secret_store, self._config.mounted_secrets_dir)
        return directory

    def _secrets_dir(self, run_id: str) -> Path | None:
        if self._config.run_secrets_dir is None:
            return None
        return self._config.run_secrets_dir / run_id

    def build_command(
        self, run_id: str, spec_path: Path, token_path: Path, secrets_dir: Path | None = None
    ) -> list[str]:
        config = self._config
        command = [
            str(config.bench_binary),
            "run",
            "--spec",
            str(spec_path),
            *output_arguments(
                str(run_bundle_dir(config.data_dir, run_id)),
                config.s3,
                # The api's own certificate, as for the api hop; the CA comes from
                # SSL_CERT_FILE, which bench inherits.
                ObjectStoreClient(
                    config.object_store_hop,
                    str(config.tls_cert_file) if config.tls_cert_file else None,
                    str(config.tls_key_file) if config.tls_key_file else None,
                ),
            ),
            "--run-id",
            run_id,
            "--api-url",
            config.api_url,
            "--run-token-file",
            str(token_path),
            "--api-tls",
            tls_flag(config.api_hop.enabled),
        ]
        if secrets_dir is not None:
            command += ["--secrets-dir", str(secrets_dir)]
        if config.engine_override:
            command += ["--engine-override", config.engine_override]
        if config.offline:
            command.append("--offline")
        if config.api_hop.client_cert:
            # Subprocess runner only (dev, single node): bench reuses the api's own
            # certificate as its client certificate. The Kubernetes runner will use
            # SERVTUNER_TLS_BENCH_SECRET instead.
            assert config.tls_cert_file and config.tls_key_file  # Settings guarantee these
            command += [
                "--tls-cert-file",
                str(config.tls_cert_file),
                "--tls-key-file",
                str(config.tls_key_file),
            ]
        return command

    def status(self, handle: RunHandle) -> RunnerStatus:
        process = self._processes.get(handle.run_id)
        if process is None:
            return RunnerStatus(
                RunnerState.FAILED,
                message="the api restarted while this run's bench was its child process, so the "
                "run ended with it; the subprocess runner cannot resume runs",
            )
        exit_code = process.poll()
        if exit_code is None:
            return RunnerStatus(RunnerState.RUNNING)
        # The leader has ended: a follower still waiting for it has nothing left to do.
        for follower in self._followers.pop(handle.run_id, []):
            _stop(follower)
        self._token_path(handle.run_id).unlink(missing_ok=True)
        if (secrets_dir := self._secrets_dir(handle.run_id)) is not None:
            remove_materialized(secrets_dir)
        if handle.run_id in self._cancelled:
            return RunnerStatus(RunnerState.CANCELLED, exit_code=exit_code)
        if exit_code == 0:
            return RunnerStatus(RunnerState.SUCCEEDED, exit_code=exit_code)
        return RunnerStatus(
            RunnerState.FAILED, exit_code=exit_code, message=f"exit code {exit_code}"
        )

    def cancel(self, handle: RunHandle) -> None:
        process = self._processes.get(handle.run_id)
        if process is None or process.poll() is not None:
            return
        self._cancelled.add(handle.run_id)
        for one in [*self._followers.get(handle.run_id, []), process]:
            _stop(one)

    def logs(self, handle: RunHandle) -> Iterator[str]:
        log_path = self._log_path(handle.run_id)
        if not log_path.is_file():
            return
        with log_path.open(encoding="utf-8", errors="replace") as log_file:
            yield from (line.rstrip("\n") for line in log_file)

    def _write_private(self, directory_name: str, run_id: str, content: bytes) -> Path:
        directory = self._config.data_dir / directory_name
        directory.mkdir(parents=True, exist_ok=True)
        path = directory / run_id
        file_descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, PRIVATE_FILE_MODE)
        with os.fdopen(file_descriptor, "wb") as private_file:
            private_file.write(content)
        return path

    def _token_path(self, run_id: str) -> Path:
        return self._config.data_dir / TOKENS_DIR_NAME / run_id

    def _log_path(self, run_id: str) -> Path:
        log_directory = self._config.data_dir / LOGS_DIR_NAME
        log_directory.mkdir(parents=True, exist_ok=True)
        return log_directory / f"{run_id}.log"


def _stop(process: subprocess.Popen[bytes]) -> None:
    """SIGTERM, then SIGKILL after the grace period."""
    if process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=CANCEL_GRACE_SECONDS)
    except subprocess.TimeoutExpired:
        process.kill()
