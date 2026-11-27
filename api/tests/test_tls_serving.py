"""The ingress/bench -> api hop: mTLS that requires client certificates, or plain HTTP."""

import dataclasses
import shutil
import socket
import ssl
from pathlib import Path
from typing import Any, cast

import httpx2
import pytest

from servtuner_api.settings.models import Settings
from tests.support.certs import CertificateAuthority, IssuedCertificate, create_authority
from tests.support.environment import platform_env
from tests.support.server import running_server
from tests.support.waiting import wait_until

RELOAD_SECONDS = 0.05


@pytest.fixture
def authority(tmp_path: Path) -> CertificateAuthority:
    return create_authority(tmp_path / "pki")


@pytest.fixture
def served_certificate(tmp_path: Path, authority: CertificateAuthority) -> IssuedCertificate:
    """The api's certificate at the paths the settings point to, as a mounted Secret would be."""
    issued = authority.issue("api-first")
    mounted = IssuedCertificate(tmp_path / "tls.crt", tmp_path / "tls.key")
    shutil.copy(issued.cert_file, mounted.cert_file)
    shutil.copy(issued.key_file, mounted.key_file)
    shutil.copy(authority.cert_file, tmp_path / "ca.crt")  # default client CA, beside tls.crt
    return mounted


def mtls_settings(
    tmp_path: Path, authority: CertificateAuthority, served: IssuedCertificate
) -> Settings:
    env = platform_env(
        tmp_path,
        SERVTUNER_TLS_HOP_BENCH_API="mtls",
        SERVTUNER_TLS_HOP_INGRESS_API="mtls",
        SERVTUNER_TLS_CERT_FILE=str(served.cert_file),
        SERVTUNER_TLS_KEY_FILE=str(served.key_file),
        SSL_CERT_FILE=str(authority.cert_file),
    )
    settings = Settings.from_env(env)
    return dataclasses.replace(settings, cert_reload_seconds=RELOAD_SECONDS)


def client_context(
    authority: CertificateAuthority, identity: IssuedCertificate | None
) -> ssl.SSLContext:
    context = ssl.create_default_context(cafile=str(authority.cert_file))
    if identity is not None:
        context.load_cert_chain(str(identity.cert_file), str(identity.key_file))
    return context


def test_mtls_accepts_a_client_with_a_certificate(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    client_identity = authority.issue("bench-client")
    settings = mtls_settings(tmp_path, authority, served_certificate)
    with running_server(settings) as server:
        context = client_context(authority, client_identity)
        response = httpx2.get(server.url("https"), verify=context)
    assert response.json() == {"status": "ok"}


def test_mtls_refuses_a_client_without_a_certificate(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    settings = mtls_settings(tmp_path, authority, served_certificate)
    with running_server(settings) as server:
        with pytest.raises(httpx2.TransportError):
            httpx2.get(server.url("https"), verify=client_context(authority, None))


def test_mtls_refuses_a_certificate_from_an_untrusted_authority(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    stranger = create_authority(tmp_path / "other-pki", "other-ca").issue("stranger")
    settings = mtls_settings(tmp_path, authority, served_certificate)
    with running_server(settings) as server:
        with pytest.raises(httpx2.TransportError):
            httpx2.get(server.url("https"), verify=client_context(authority, stranger))


def test_client_certificate_from_a_ca_in_ssl_cert_file_but_not_the_client_ca_is_refused(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    # SSL_CERT_FILE (outbound trust) holds the stranger's CA, as it would hold public CAs.
    other = create_authority(tmp_path / "other-pki", "other-ca")
    bundle = tmp_path / "bundle.crt"
    bundle.write_text(authority.cert_file.read_text() + other.cert_file.read_text())
    settings = mtls_settings(tmp_path, authority, served_certificate)
    tls = dataclasses.replace(settings.tls, ca_bundle_file=bundle)
    with running_server(dataclasses.replace(settings, tls=tls)) as server:
        stranger = other.issue("stranger")
        with pytest.raises(httpx2.TransportError):
            httpx2.get(server.url("https"), verify=client_context(authority, stranger))
        trusted = authority.issue("bench-client")
        response = httpx2.get(server.url("https"), verify=client_context(authority, trusted))
    assert response.status_code == 200


def test_mtls_server_refuses_plain_http(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    settings = mtls_settings(tmp_path, authority, served_certificate)
    with running_server(settings) as server, pytest.raises(httpx2.TransportError):
        httpx2.get(server.url("http"))


def test_platform_mode_serves_plain_http(tmp_path: Path) -> None:
    with running_server(Settings.from_env(platform_env(tmp_path))) as server:
        assert httpx2.get(server.url("http")).json() == {"status": "ok"}
        with pytest.raises(httpx2.TransportError):
            httpx2.get(server.url("https"))


def test_rotated_certificate_is_served_without_a_restart(
    tmp_path: Path, authority: CertificateAuthority, served_certificate: IssuedCertificate
) -> None:
    settings = mtls_settings(tmp_path, authority, served_certificate)
    with running_server(settings) as server:
        assert served_common_name(authority, server.port) == "api-first"
        rotated = authority.issue("api-second")
        shutil.copy(rotated.key_file, served_certificate.key_file)
        shutil.copy(rotated.cert_file, served_certificate.cert_file)
        wait_until(
            lambda: serves_common_name(authority, server.port, "api-second"),
            "the rotated certificate to be served",
        )


def serves_common_name(authority: CertificateAuthority, port: int, expected: str) -> bool:
    """True once the server presents the expected certificate. The test writes the new key and
    certificate one file after the other, so a reload can briefly pair the new key with the old
    certificate and fail the handshake; that is "not yet", not a failure. (Kubernetes swaps a
    mounted Secret's files atomically, so a real rotation has no such window.)"""
    try:
        return served_common_name(authority, port) == expected
    except ssl.SSLError, ConnectionError:
        return False


def served_common_name(authority: CertificateAuthority, port: int) -> str:
    identity = authority.issue("probe-client")
    context = client_context(authority, identity)
    with (
        socket.create_connection(("localhost", port)) as connection,
        context.wrap_socket(connection, server_hostname="localhost") as tls,
    ):
        # typeshed's peer-certificate type does not describe the nested subject tuples.
        certificate = cast(dict[str, Any], tls.getpeercert())
        common_names = [
            str(value)
            for relative_name in certificate["subject"]
            for key, value in relative_name
            if key == "commonName"
        ]
        return common_names[0]
