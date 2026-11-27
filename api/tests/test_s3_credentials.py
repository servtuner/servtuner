"""Ambient credentials come from botocore's default chain, as EKS Pod Identity needs.

A fake agent and a fake S3 endpoint show the chain fetched the agent's keys and signed with them.
"""

import json
import threading
from collections.abc import Iterator
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from typing import Any, override

import pytest

from servtuner_api.bundles import MissingCredentialsError, S3BundleStore
from servtuner_api.settings.models import (
    ChecksumMode,
    CredentialsSource,
    S3Settings,
)
from tests.support.tls import plain_tls

TOKEN = "pod-identity-token"
seen: dict[str, str] = {}


class AgentHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.headers.get("Authorization") != TOKEN:
            self.send_response(401)
            self.end_headers()
            return
        expiration = (datetime.now(UTC) + timedelta(hours=1)).strftime("%Y-%m-%dT%H:%M:%SZ")
        body = json.dumps(
            {
                "AccessKeyId": "ASIAPODIDENTITY",
                "SecretAccessKey": "pod-secret",
                "Token": "pod-session-token",
                "Expiration": expiration,
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)

    @override
    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - the base class's name
        pass


class S3Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        seen["authorization"] = self.headers.get("Authorization", "")
        seen["token"] = self.headers.get("X-Amz-Security-Token", "")
        body = (
            b'<?xml version="1.0" encoding="UTF-8"?>'
            b"<ListBucketResult><Name>b</Name><IsTruncated>false</IsTruncated></ListBucketResult>"
        )
        self.send_response(200)
        self.send_header("Content-Type", "application/xml")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    @override
    def log_message(self, format: str, *args: Any) -> None:  # noqa: A002 - the base class's name
        pass


def serve(handler: type[BaseHTTPRequestHandler]) -> Iterator[str]:
    server = HTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}"
    finally:
        server.shutdown()


@pytest.fixture
def agent() -> Iterator[str]:
    yield from serve(AgentHandler)


@pytest.fixture
def fake_s3() -> Iterator[str]:
    yield from serve(S3Handler)


def test_ambient_credentials_come_from_the_container_credentials_endpoint(
    agent: str, fake_s3: str, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    token_file = tmp_path / "eks-pod-identity-token"
    token_file.write_text(TOKEN)
    for name in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_PROFILE"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", f"{agent}/v1/credentials")
    monkeypatch.setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", str(token_file))
    monkeypatch.setenv("AWS_CONFIG_FILE", str(tmp_path / "none"))
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(tmp_path / "none"))
    monkeypatch.setenv("AWS_EC2_METADATA_DISABLED", "true")
    settings = S3Settings(
        endpoint=fake_s3,
        region="us-east-1",
        bucket="b",
        prefix="runs/",
        force_path_style=True,
        checksum_mode=ChecksumMode.WHEN_REQUIRED,
        credentials_source=CredentialsSource.AMBIENT,
    )
    tls = plain_tls()
    store = S3BundleStore(settings, tls)
    assert store.list_files("01ARZ3NDEKTSV4RRFFQ69G5FAV") == []
    assert "Credential=ASIAPODIDENTITY/" in seen["authorization"]
    assert seen["token"] == "pod-session-token"


def test_ambient_with_no_credentials_fails_at_startup(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Fail fast with a clear message, never fall back to anonymous access."""
    for name in (
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "AWS_SESSION_TOKEN",
        "AWS_PROFILE",
        "AWS_CONTAINER_CREDENTIALS_FULL_URI",
        "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
        "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
        "AWS_WEB_IDENTITY_TOKEN_FILE",
        "AWS_ROLE_ARN",
    ):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("AWS_CONFIG_FILE", str(tmp_path / "none"))
    monkeypatch.setenv("AWS_SHARED_CREDENTIALS_FILE", str(tmp_path / "none"))
    monkeypatch.setenv("AWS_EC2_METADATA_DISABLED", "true")
    settings = S3Settings(
        endpoint="http://s3.invalid",
        region="us-east-1",
        bucket="b",
        prefix="runs/",
        force_path_style=True,
        checksum_mode=ChecksumMode.WHEN_REQUIRED,
        credentials_source=CredentialsSource.AMBIENT,
    )
    tls = plain_tls()
    with pytest.raises(MissingCredentialsError, match="no AWS credentials resolved"):
        S3BundleStore(settings, tls)
