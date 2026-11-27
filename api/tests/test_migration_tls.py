"""Migration 0002 rewrites the tls blocks servtuner 0.1 stored, and takes them back."""

import json
from collections.abc import Iterator
from typing import Any

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import Engine, text
from testcontainers.community.postgres import PostgresContainer

from servtuner_api.database import MIGRATIONS_DIR, create_database_engine

DATABASE = "tls_migration"
NOW = "2026-11-27T00:00:00+00:00"

# A saved target, a managed target and a schedule as servtuner 0.1 stored them.
TARGET_V1 = {
    "type": "litellm",
    "endpoint": "https://litellm.example.svc:4000/v1",
    "model": "m",
    "tls": {"mode": "mtls", "secretRef": {"name": "servtuner-client"}},
    "upstreams": [{"name": "u", "tls": {"mode": "tls", "caSecretRef": {"name": "u-ca"}}}],
}
TARGET_V2 = {
    "type": "litellm",
    "endpoint": "https://litellm.example.svc:4000/v1",
    "model": "m",
    "tls": {"enabled": True, "clientCert": {"secretRef": {"name": "servtuner-client"}}},
    "upstreams": [{"name": "u", "tls": {"enabled": True, "caSecretRef": {"name": "u-ca"}}}],
}
SPEC_V1 = {
    "apiVersion": "servtuner.dev/v1",
    "kind": "RunSpec",
    "name": "nightly",
    "target": {"type": "vllm", "endpoint": "http://v.example.svc/v1", "tls": {"mode": "platform"}},
}


def migrate(engine: Engine, revision: str, *, down: bool = False) -> None:
    config = Config()
    config.set_main_option("script_location", str(MIGRATIONS_DIR))
    with engine.begin() as connection:
        config.attributes["connection"] = connection
        (command.downgrade if down else command.upgrade)(config, revision)


@pytest.fixture
def database(postgres: PostgresContainer) -> Iterator[Engine]:
    admin = create_database_engine(postgres.get_connection_url())
    with admin.connect().execution_options(isolation_level="AUTOCOMMIT") as connection:
        connection.execute(text(f"DROP DATABASE IF EXISTS {DATABASE}"))
        connection.execute(text(f"CREATE DATABASE {DATABASE}"))
    admin.dispose()
    url = postgres.get_connection_url().rsplit("/", 1)[0] + f"/{DATABASE}"
    engine = create_database_engine(url)
    migrate(engine, "0001")
    yield engine
    engine.dispose()


def insert_v1_rows(engine: Engine, managed_transport: dict[str, object]) -> None:
    with engine.begin() as connection:
        connection.execute(
            text(
                "INSERT INTO targets (id, name, target_type, target, credentials, created_at, "
                "updated_at) VALUES ('t1', 'proxy', 'litellm', CAST(:target AS jsonb), '{}', "
                ":now, :now)"
            ),
            {"target": json.dumps(TARGET_V1), "now": NOW},
        )
        connection.execute(
            text(
                "INSERT INTO managed_targets (id, name, namespace, spec, created_at, updated_at) "
                "VALUES ('m1', 'qwen', 'servtuner-tune-qwen', CAST(:spec AS jsonb), :now, :now)"
            ),
            {"spec": json.dumps({"model": "q", "transport": managed_transport}), "now": NOW},
        )
        connection.execute(
            text(
                "INSERT INTO schedules (id, name, spec_raw, every_seconds, start_at, next_run_at, "
                "is_enabled, regression_tolerance, created_at) "
                "VALUES ('s1', 'nightly', :spec, 86400, :now, :now, true, 0.1, :now)"
            ),
            {"spec": json.dumps(SPEC_V1).encode(), "now": NOW},
        )


def stored(engine: Engine) -> tuple[Any, Any, Any]:
    with engine.connect() as connection:
        target = connection.execute(text("SELECT target FROM targets")).scalar_one()
        spec = connection.execute(text("SELECT spec FROM managed_targets")).scalar_one()
        raw = connection.execute(text("SELECT spec_raw FROM schedules")).scalar_one()
    return target, spec["transport"], json.loads(raw)


def test_upgrade_rewrites_and_downgrade_restores(database: Engine) -> None:
    insert_v1_rows(database, {"mode": "tls", "secret": "qwen-cert"})
    migrate(database, "0002")
    target, transport, schedule = stored(database)
    assert target == TARGET_V2
    assert transport == {"enabled": True, "client_auth": "none", "secret": "qwen-cert"}
    assert schedule["apiVersion"] == "servtuner.dev/v2"
    assert schedule["target"]["tls"] == {"enabled": False}

    migrate(database, "0001", down=True)
    target, transport, schedule = stored(database)
    assert target == TARGET_V1
    assert transport == {"mode": "tls", "secret": "qwen-cert"}
    assert schedule == SPEC_V1


def test_plain_managed_transport(database: Engine) -> None:
    insert_v1_rows(database, {"mode": "platform"})
    migrate(database, "0002")
    assert stored(database)[1] == {"enabled": False}


def test_downgrade_refuses_what_0_1_cannot_express(database: Engine) -> None:
    insert_v1_rows(database, {"mode": "platform"})
    migrate(database, "0002")
    with_ca = {**TARGET_V2, "tls": {**TARGET_V2["tls"], "caSecretRef": {"name": "ca"}}}
    with database.begin() as connection:
        connection.execute(
            text("UPDATE targets SET target = CAST(:target AS jsonb)"),
            {"target": json.dumps(with_ca)},
        )
    with pytest.raises(RuntimeError, match="no servtuner 0.1 form"):
        migrate(database, "0001", down=True)
