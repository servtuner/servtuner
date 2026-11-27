"""Saved targets: CRUD, write-only keys and Check connection.

The keys here are fakes, but the tests treat them as real: after a key is set,
no response, OpenAPI document, validation error or log line may contain it.
A stand-in bench answers check-target and records which key it was handed.
"""

import json
import logging
import stat
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.secret_store import FileSecretStore, SecretStore
from servtuner_api.targets.checker import TargetChecker
from servtuner_api.targets.service import TargetService
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner

KEY = "sk-live-0123456789abcdefghijKLMN"  # 32 characters: the preview shows 4 + 4
ONE_TIME_KEY = "sk-one-time-9876543210zyxwvutsr"
PASSWORD = "admin-password-0123456789"

CHECK_RESULT = {
    "status": "ok",
    "checked_at": "2026-11-22T21:00:00Z",
    "target_type": "vllm",
    "endpoint": "http://vllm.example:8080/v1",
    "checks": [{"name": "auth", "status": "ok", "message": "200", "duration_seconds": 0.01}],
}

# Records the request and the key it found, then prints a check result.
FAKE_BENCH = f"""#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    --input) cp "$2" "$RECORD_DIR/request.json" ;;
    --secrets-dir) dir="$2"; echo "$2" > "$RECORD_DIR/secrets-dir.txt" ;;
  esac
  shift
done
cat "$dir"/*/token > "$RECORD_DIR/key-seen.txt" 2>/dev/null
echo '{json.dumps(CHECK_RESULT)}'
"""


@pytest.fixture
def record_dir(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    directory = tmp_path / "record"
    directory.mkdir()
    monkeypatch.setenv("RECORD_DIR", str(directory))
    return directory


def make_client(
    engine: Engine, data_dir: Path, tmp_path: Path, store: SecretStore | None
) -> Iterator[TestClient]:
    bench = tmp_path / "bench"
    bench.write_text(FAKE_BENCH)
    bench.chmod(bench.stat().st_mode | stat.S_IXUSR)

    def service_with_targets():  # type: ignore[no-untyped-def]
        service = build_service(engine, FakeRunner(), data_dir)
        checker = TargetChecker(bench, tmp_path / "run-secrets", store, None, "the api pod test")
        service.targets = TargetService(
            service._repository,  # noqa: SLF001 - the test assembles the service by hand
            store,
            checker,
            {"url": "http://prometheus.example:9090", "tls": {"enabled": False}},
        )
        return service

    with TestClient(create_app(service_with_targets)) as client:
        yield client


@pytest.fixture
def store(tmp_path: Path) -> FileSecretStore:
    return FileSecretStore(tmp_path / "store")


@pytest.fixture
def targets(
    engine: Engine, data_dir: Path, tmp_path: Path, store: FileSecretStore, record_dir: Path
) -> Iterator[TestClient]:
    yield from make_client(engine, data_dir, tmp_path, store)


VLLM: dict[str, Any] = {
    "name": "vllm-lan",
    "description": "RTX 3070",
    "target": {
        "type": "vllm",
        "endpoint": "http://vllm.example:8080/v1",
        "model": "tiny",
        "metrics": {"namespace": "servtuner-external", "pod": "vllm-lan", "node": "vllm-lan-gpu"},
        "tls": {"enabled": False},
    },
}


def create(client: TestClient, body: dict[str, Any] = VLLM) -> dict[str, Any]:
    response = client.post("/targets", json=body)
    assert response.status_code == 201, response.text
    created: dict[str, Any] = response.json()
    return created


def everything_readable(client: TestClient, target_id: object) -> str:
    """Every response a reader can get, as one string."""
    return "".join(
        [
            client.get("/targets").text,
            client.get(f"/targets/{target_id}").text,
            json.dumps(client.get("/openapi.json").json()),
        ]
    )


def test_a_target_is_saved_without_keys_and_says_so(targets: TestClient) -> None:
    created = create(targets)
    assert created["name"] == "vllm-lan"
    assert created["slots"] == ["api_key"]
    assert created["credentials"] == {"api_key": {"is_set": False}}
    assert "auth" not in created["target"]
    assert targets.get("/targets").json()[0]["id"] == created["id"]


def test_a_key_in_the_target_body_is_refused(targets: TestClient) -> None:
    body = {**VLLM, "target": {**VLLM["target"], "auth": {"secretRef": {"name": "x", "key": "k"}}}}
    response = targets.post("/targets", json=body)
    assert response.status_code == 422
    assert "credentials" in response.text


def test_names_are_unique(targets: TestClient) -> None:
    create(targets)
    assert targets.post("/targets", json=VLLM).status_code == 409


def test_a_set_key_is_stored_and_never_returned(
    targets: TestClient, store: FileSecretStore, caplog: pytest.LogCaptureFixture
) -> None:
    target_id = create(targets)["id"]
    with caplog.at_level(logging.DEBUG):
        response = targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})
    assert response.status_code == 204
    assert response.content == b""

    secret_name = f"servtuner-target-{str(target_id).lower()}-api-key"
    assert store.read(secret_name) == {"token": KEY}
    shown = targets.get(f"/targets/{target_id}").json()
    assert shown["credentials"]["api_key"] == {
        "is_set": True,
        "source": "managed",
        "form": "token",
        "preview": "sk-l" + "*" * 24 + "KLMN",
        "set_at": shown["credentials"]["api_key"]["set_at"],
    }
    assert shown["target"]["auth"] == {"secretRef": {"name": secret_name, "key": "token"}}
    assert KEY not in everything_readable(targets, target_id)
    assert KEY not in caplog.text


def test_a_rejected_key_body_does_not_echo_the_key(targets: TestClient) -> None:
    target_id = create(targets)["id"]
    for body in ({"value": KEY, "surprise": 1}, {"value": KEY, "external": {"name": "x"}}):
        response = targets.put(f"/targets/{target_id}/credentials/api_key", json=body)
        assert response.status_code == 422, response.text
        assert KEY not in response.text


@pytest.mark.parametrize(
    ("key", "preview"),
    [
        ("0123456789abcdefghijklmnopq", "0123" + "*" * 24 + "nopq"),
        ("0123456789abcdef", "*" * 28 + "cdef"),
        ("short-key", "*" * 32),
    ],
)
def test_short_keys_show_less(targets: TestClient, key: str, preview: str) -> None:
    target_id = create(targets)["id"]
    targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": key})
    shown = targets.get(f"/targets/{target_id}").json()["credentials"]["api_key"]["preview"]
    assert shown == preview
    assert len(shown) == 32, "the fixed width hides the key's length"


def test_clearing_a_key_deletes_its_secret(targets: TestClient, store: FileSecretStore) -> None:
    target_id = create(targets)["id"]
    targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})
    assert targets.delete(f"/targets/{target_id}/credentials/api_key").status_code == 204
    assert store.read(f"servtuner-target-{str(target_id).lower()}-api-key") is None
    shown = targets.get(f"/targets/{target_id}").json()
    assert shown["credentials"]["api_key"] == {"is_set": False}
    assert "auth" not in shown["target"]


def test_an_external_secret_is_named_not_managed(
    targets: TestClient, store: FileSecretStore
) -> None:
    store.put("my-gitops-key", {"api-key": KEY}, "user")
    target_id = create(targets)["id"]
    response = targets.put(
        f"/targets/{target_id}/credentials/api_key",
        json={"external": {"name": "my-gitops-key", "key": "api-key"}},
    )
    assert response.status_code == 204, response.text
    shown = targets.get(f"/targets/{target_id}").json()
    assert shown["credentials"]["api_key"]["source"] == "external"
    assert shown["credentials"]["api_key"]["secret_name"] == "my-gitops-key"
    assert shown["target"]["auth"] == {"secretRef": {"name": "my-gitops-key", "key": "api-key"}}
    targets.delete(f"/targets/{target_id}")
    assert store.read("my-gitops-key") == {"api-key": KEY}, "the user's Secret is never deleted"


BIFROST: dict[str, Any] = {
    "name": "bifrost",
    "target": {
        "type": "bifrost",
        "endpoint": "http://bifrost.example:8080/v1",
        "model": "mock",
        "tls": {"enabled": False},
    },
}


def test_slots_follow_the_type(targets: TestClient, store: FileSecretStore) -> None:
    vllm_id = create(targets)["id"]
    response = targets.put(f"/targets/{vllm_id}/credentials/admin", json={"value": KEY})
    assert response.status_code == 422
    assert "api_key" in response.text

    bifrost_id = create(targets, BIFROST)["id"]
    response = targets.put(
        f"/targets/{bifrost_id}/credentials/admin",
        json={"username": "admin", "password": PASSWORD},
    )
    assert response.status_code == 204, response.text
    admin = targets.get(f"/targets/{bifrost_id}").json()["credentials"]["admin"]
    assert (admin["form"], admin["username"]) == ("basic", "admin")
    assert PASSWORD not in everything_readable(targets, bifrost_id)

    # Becoming a vLLM target drops the admin slot and its Secret.
    renamed = {**BIFROST, "target": {**BIFROST["target"], "type": "vllm"}}
    assert targets.put(f"/targets/{bifrost_id}", json=renamed).status_code == 200
    assert store.read(f"servtuner-target-{str(bifrost_id).lower()}-admin") is None
    assert "admin" not in targets.get(f"/targets/{bifrost_id}").json()["credentials"]


def test_without_a_secret_store_only_external_keys_are_accepted(
    engine: Engine, data_dir: Path, tmp_path: Path, record_dir: Path
) -> None:
    for client in make_client(engine, data_dir, tmp_path, None):
        target_id = create(client)["id"]
        managed = client.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})
        assert managed.status_code == 503
        assert "external" in managed.json()["detail"]
        external = client.put(
            f"/targets/{target_id}/credentials/api_key",
            json={"external": {"name": "mine", "key": "token"}},
        )
        assert external.status_code == 204


def test_check_uses_the_stored_key_and_keeps_the_result(
    targets: TestClient, record_dir: Path
) -> None:
    target_id = create(targets)["id"]
    targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})

    response = targets.post(f"/targets/{target_id}/check", json={})
    assert response.status_code == 200, response.text
    assert response.json()["result"]["status"] == "ok"
    assert response.json()["checked_from"] == "the api pod test"
    assert (record_dir / "key-seen.txt").read_text() == KEY
    request = json.loads((record_dir / "request.json").read_text())
    assert request["prometheus"]["url"] == "http://prometheus.example:9090"
    assert KEY not in json.dumps(request), "the request names the Secret, never the key"
    secrets_dir = Path((record_dir / "secrets-dir.txt").read_text().strip())
    assert not secrets_dir.exists(), "the check's secrets are removed afterwards"
    assert targets.get(f"/targets/{target_id}").json()["last_check"]["result"]["status"] == "ok"
    assert KEY not in response.text


def test_a_one_time_key_is_used_once_and_never_kept(
    targets: TestClient, record_dir: Path, store: FileSecretStore
) -> None:
    target_id = create(targets)["id"]
    targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})
    response = targets.post(f"/targets/{target_id}/check", json={"api_key": ONE_TIME_KEY})
    assert response.status_code == 200, response.text
    assert (record_dir / "key-seen.txt").read_text() == ONE_TIME_KEY
    assert store.read(f"servtuner-target-{str(target_id).lower()}-api-key") == {"token": KEY}
    shown = targets.get(f"/targets/{target_id}").json()
    assert "last_check" not in shown, "a one-time key's result is not kept"
    assert ONE_TIME_KEY not in everything_readable(targets, target_id)
    assert ONE_TIME_KEY not in response.text


def test_an_unsaved_target_is_checked_with_the_secrets_it_names(
    targets: TestClient, record_dir: Path, store: FileSecretStore
) -> None:
    store.put("handmade", {"token": KEY}, "user")
    target = {**VLLM["target"], "auth": {"secretRef": {"name": "handmade", "key": "token"}}}
    response = targets.post("/targets/check", json={"target": target, "generate": True})
    assert response.status_code == 200, response.text
    assert (record_dir / "key-seen.txt").read_text() == KEY
    assert json.loads((record_dir / "request.json").read_text())["generate"] is True


def test_deleting_a_target_deletes_its_managed_secrets(
    targets: TestClient, store: FileSecretStore
) -> None:
    target_id = create(targets)["id"]
    targets.put(f"/targets/{target_id}/credentials/api_key", json={"value": KEY})
    assert targets.delete(f"/targets/{target_id}").status_code == 204
    assert targets.get(f"/targets/{target_id}").status_code == 404
    assert store.read(f"servtuner-target-{str(target_id).lower()}-api-key") is None
