"""The production wiring (``build_app``) end to end: real Postgres, real SubprocessRunner."""

import stat
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine
from testcontainers.community.postgres import PostgresContainer

from servtuner_api.server import build_app
from servtuner_api.settings.models import Settings
from tests.support.environment import plain_env
from tests.support.specs import run_spec_bytes
from tests.support.waiting import wait_until

# A stand-in bench that exits with the code the test chose.
FAKE_BENCH = '#!/bin/sh\nexit "${BENCH_EXIT_CODE:-0}"\n'


def settings_for(tmp_path: Path, postgres: PostgresContainer) -> Settings:
    bench = tmp_path / "bench"
    bench.write_text(FAKE_BENCH)
    bench.chmod(bench.stat().st_mode | stat.S_IXUSR)
    return Settings.from_env(
        plain_env(
            tmp_path,
            SERVTUNER_BENCH_BINARY=str(bench),
            SERVTUNER_DATABASE_HOST=postgres.get_container_host_ip(),
            SERVTUNER_DATABASE_PORT=str(postgres.get_exposed_port(5432)),
            SERVTUNER_DATABASE_NAME=postgres.dbname,
            SERVTUNER_DATABASE_USER=postgres.username,
            SERVTUNER_DATABASE_PASSWORD=postgres.password,
            SERVTUNER_AUTH_MODE="none",  # these tests are about runs, not users
        )
    )


@pytest.mark.parametrize(("exit_code", "final_status"), [("0", "succeeded"), ("3", "failed")])
def test_a_run_ends_with_the_process_exit_status(
    tmp_path: Path,
    postgres: PostgresContainer,
    engine: Engine,
    monkeypatch: pytest.MonkeyPatch,
    exit_code: str,
    final_status: str,
) -> None:
    monkeypatch.setenv("BENCH_EXIT_CODE", exit_code)
    settings = settings_for(tmp_path, postgres)
    with TestClient(build_app(settings)) as client:
        run_id = client.post("/runs", content=run_spec_bytes()).json()["id"]
        wait_until(
            lambda: client.get(f"/runs/{run_id}").json()["status"] == final_status,
            f"run to be {final_status}",
        )
