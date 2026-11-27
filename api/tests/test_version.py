"""GET /version: the build stamped into the image, for signed-in users only."""

from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.version import DEV_VERSION, UNKNOWN_COMMIT, build_info
from tests.support.auth import PASSWORD, SETUP_TOKEN, auth_app, auth_settings

COMMIT = "0123456789abcdef0123456789abcdef01234567"


@pytest.fixture(autouse=True)
def fresh_build_info() -> Iterator[None]:
    build_info.cache_clear()
    yield
    build_info.cache_clear()


def test_an_unstamped_build_says_so(client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("SERVTUNER_VERSION", raising=False)
    monkeypatch.delenv("SERVTUNER_COMMIT", raising=False)
    assert client.get("/version").json() == {"version": DEV_VERSION, "commit": UNKNOWN_COMMIT}


def test_the_stamped_build_is_reported(client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("SERVTUNER_VERSION", "0.2.0")
    monkeypatch.setenv("SERVTUNER_COMMIT", COMMIT)
    assert client.get("/version").json() == {"version": "0.2.0", "commit": COMMIT}


def test_version_needs_a_signed_in_user(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as app:
        setup = {"username": "first-admin", "password": PASSWORD}
        created = app.client.post(
            "/auth/setup", json=setup, headers={"X-Servtuner-Setup-Token": SETUP_TOKEN}
        )
        assert created.status_code == 201
        assert app.client.get("/version").status_code == 200
        app.client.cookies.clear()
        assert app.client.get("/version").status_code == 401
