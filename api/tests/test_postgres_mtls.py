"""The api -> Postgres hop: ``verify-full`` with a client certificate, or ``disable``.

The container accepts only TLS connections that present a client certificate
whose CN is the database user, so a pass here means the api really did mTLS.
"""

import dataclasses
from collections.abc import Iterator
from pathlib import Path

import pytest
from sqlalchemy import text
from sqlalchemy.exc import OperationalError
from testcontainers.core.container import DockerContainer

from servtuner_api.database import build_database_url, create_database_engine, upgrade_database
from servtuner_api.settings.models import DatabaseSslMode, Settings
from tests.support.certs import LOOPBACK, CertificateAuthority, create_authority
from tests.support.environment import platform_env
from tests.support.postgres_tls import (
    DATABASE_NAME,
    DATABASE_USER,
    POSTGRES_PORT,
    tls_postgres_container,
)


@pytest.fixture(scope="module")
def authority(tmp_path_factory: pytest.TempPathFactory) -> CertificateAuthority:
    return create_authority(tmp_path_factory.mktemp("pg-pki"))


@pytest.fixture(scope="module")
def tls_postgres(
    docker_required: None, authority: CertificateAuthority, tmp_path_factory: pytest.TempPathFactory
) -> Iterator[DockerContainer]:
    with tls_postgres_container(authority, tmp_path_factory.mktemp("pg-mount")) as container:
        yield container


def api_settings(
    tmp_path: Path, authority: CertificateAuthority, container: DockerContainer, **env: str
) -> Settings:
    client = authority.issue(DATABASE_USER)
    return Settings.from_env(
        platform_env(
            tmp_path,
            SERVTUNER_TLS_MODE="mtls",
            SERVTUNER_TLS_CERT_FILE=str(client.cert_file),
            SERVTUNER_TLS_KEY_FILE=str(client.key_file),
            SSL_CERT_FILE=str(authority.cert_file),
            SERVTUNER_TLS_CLIENT_CA_FILE=str(authority.cert_file),
            SERVTUNER_DATABASE_SSLMODE="verify-full",
            SERVTUNER_DATABASE_PASSWORD="",
            SERVTUNER_DATABASE_HOST=LOOPBACK,
            SERVTUNER_DATABASE_PORT=str(container.get_exposed_port(POSTGRES_PORT)),
            SERVTUNER_DATABASE_NAME=DATABASE_NAME,
            **env,
        )
    )


def test_verify_full_with_a_client_certificate_connects_and_migrates(
    tmp_path: Path, authority: CertificateAuthority, tls_postgres: DockerContainer
) -> None:
    settings = api_settings(tmp_path, authority, tls_postgres)
    engine = create_database_engine(build_database_url(settings.database, settings.tls))
    upgrade_database(engine)
    with engine.connect() as connection:
        query = "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()"
        assert connection.execute(text(query)).scalar_one() is True
    engine.dispose()


def test_postgres_refuses_a_connection_without_a_client_certificate(
    tmp_path: Path, authority: CertificateAuthority, tls_postgres: DockerContainer
) -> None:
    settings = api_settings(tmp_path, authority, tls_postgres)
    url = build_database_url(settings.database, settings.tls).difference_update_query(
        ["sslcert", "sslkey"]
    )
    with pytest.raises(OperationalError), create_database_engine(url).connect():
        pass


def test_postgres_refuses_a_plain_connection(
    tmp_path: Path, authority: CertificateAuthority, tls_postgres: DockerContainer
) -> None:
    settings = api_settings(tmp_path, authority, tls_postgres)
    plain = dataclasses.replace(settings.database, ssl_mode=DatabaseSslMode.DISABLE)
    with (
        pytest.raises(OperationalError),
        create_database_engine(build_database_url(plain, settings.tls)).connect(),
    ):
        pass
