"""Migrations match the models, and the connection URL honours the two modes."""

from pathlib import Path

from alembic.autogenerate import compare_metadata
from alembic.migration import MigrationContext
from sqlalchemy import inspect
from sqlalchemy.engine import Engine

from servtuner_api.database import Base, build_database_url
from servtuner_api.settings.models import Settings
from tests.support.environment import plain_env
from tests.test_settings import mtls_env


def test_migrations_create_every_table_and_match_the_models(migrated_engine: Engine) -> None:
    assert {"runs", "run_events", "experiments", "run_metrics"} <= set(
        inspect(migrated_engine).get_table_names()
    )
    with migrated_engine.connect() as connection:
        context = MigrationContext.configure(connection)
        assert compare_metadata(context, Base.metadata) == []


def test_platform_url_is_sslmode_disable_with_a_password(tmp_path: Path) -> None:
    settings = Settings.from_env(plain_env(tmp_path))
    url = build_database_url(settings.database, settings.tls)
    assert dict(url.query) == {"sslmode": "disable"}
    assert url.password == "not-a-real-password"


def test_mtls_url_is_verify_full_with_the_client_certificate(tmp_path: Path) -> None:
    settings = Settings.from_env(mtls_env(tmp_path))
    url = build_database_url(settings.database, settings.tls)
    assert dict(url.query) == {
        "sslmode": "verify-full",
        "sslcert": str(tmp_path / "tls.crt"),
        "sslkey": str(tmp_path / "tls.key"),
        "sslrootcert": str(tmp_path / "ca-bundle.crt"),
    }
    assert url.password is None
