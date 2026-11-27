"""Shared fixtures: a real Postgres (testcontainers), a fake Runner, an app client."""

import shutil
import subprocess
from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import text
from sqlalchemy.engine import Engine
from testcontainers.community.postgres import PostgresContainer
from testcontainers.core.docker_client import DockerClient

from servtuner_api.app import create_app
from servtuner_api.bundles import FileBundleStore
from servtuner_api.database import create_database_engine, create_session_factory, upgrade_database
from servtuner_api.records.repository import RunRepository
from servtuner_api.service import RunService
from tests.support.fake_runner import FakeRunner

POSTGRES_IMAGE = "postgres:17"
RUNNER_POLL_SECONDS = 0.02
SCHEDULER_POLL_SECONDS = 3600.0
TABLES = (
    "tuning_sessions, managed_targets, hosts, price_book, price_book_datasheet, targets, "
    "schedules, experiment_analyses, run_metrics, run_events, runs, experiments, "
    "user_sessions, user_identities, audit_events, login_failures, users"
)


def _docker_is_available() -> bool:
    try:
        DockerClient().client.ping()
    except Exception:  # any failure means the testcontainers tests cannot run
        return False
    return True


@pytest.fixture(scope="session")
def docker_required() -> None:
    if not _docker_is_available():
        pytest.skip("docker is unavailable: the testcontainers Postgres tests cannot run")


@pytest.fixture(scope="session")
def postgres(docker_required: None) -> Iterator[PostgresContainer]:
    with PostgresContainer(POSTGRES_IMAGE, driver="psycopg") as container:
        yield container


@pytest.fixture(scope="session")
def migrated_engine(postgres: PostgresContainer) -> Iterator[Engine]:
    engine = create_database_engine(postgres.get_connection_url())
    upgrade_database(engine)
    yield engine
    engine.dispose()


@pytest.fixture
def engine(migrated_engine: Engine) -> Iterator[Engine]:
    yield migrated_engine
    with migrated_engine.begin() as connection:
        connection.execute(text(f"TRUNCATE {TABLES} RESTART IDENTITY CASCADE"))


@pytest.fixture
def fake_runner() -> FakeRunner:
    return FakeRunner()


@pytest.fixture
def data_dir(tmp_path: Path) -> Path:
    return tmp_path / "data"


def build_service(
    engine: Engine, runner: FakeRunner, data_dir: Path, **overrides: float
) -> RunService:
    return RunService(
        repository=RunRepository(create_session_factory(engine)),
        runner=runner,
        store=FileBundleStore(data_dir),
        run_token_ttl_seconds=overrides.get("run_token_ttl_seconds", 3600),
        runner_poll_seconds=RUNNER_POLL_SECONDS,
        # Tests drive schedules with explicit clocks (run_due_schedules), never the loop.
        scheduler_poll_seconds=SCHEDULER_POLL_SECONDS,
        tuning_poll_seconds=SCHEDULER_POLL_SECONDS,  # tests tick sessions explicitly
    )


@pytest.fixture
def client(engine: Engine, fake_runner: FakeRunner, data_dir: Path) -> Iterator[TestClient]:
    app = create_app(lambda: build_service(engine, fake_runner, data_dir))
    with TestClient(app) as test_client:
        yield test_client


BENCH_MODULE = Path(__file__).resolve().parents[2] / "bench"


@pytest.fixture(scope="session")
def bench_binary(tmp_path_factory: pytest.TempPathFactory) -> Path:
    """The real bench, built once per session when Go is available (else the test is skipped)."""
    if shutil.which("go") is None:
        pytest.skip("go is unavailable: the real bench cannot be built")
    binary = tmp_path_factory.mktemp("bench") / "bench"
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/servtuner"], cwd=BENCH_MODULE, check=True
    )  # noqa: S607
    return binary
