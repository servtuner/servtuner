"""The secret store backends and materializing a run's Secrets."""

import base64
import logging
import stat
from pathlib import Path
from types import SimpleNamespace
from typing import Any

import pytest
from lightkube.core.exceptions import ApiError

from servtuner_api.secret_store import (
    FileSecretStore,
    KubernetesSecretStore,
    materialize,
    secret_names_in,
)


def test_file_store_replaces_a_secret_wholly(tmp_path: Path) -> None:
    store = FileSecretStore(tmp_path)
    store.put("servtuner-target-a-admin", {"token": "t1"}, "A")
    store.put("servtuner-target-a-admin", {"username": "u", "password": "p"}, "A")
    assert store.read("servtuner-target-a-admin") == {"password": "p", "username": "u"}
    assert (
        stat.S_IMODE((tmp_path / "servtuner-target-a-admin" / "username").stat().st_mode) == 0o600
    )
    store.delete("servtuner-target-a-admin")
    assert store.read("servtuner-target-a-admin") is None
    store.delete("servtuner-target-a-admin")  # deleting twice is fine


def test_file_store_warns_that_it_is_for_development(
    tmp_path: Path, caplog: pytest.LogCaptureFixture
) -> None:
    with caplog.at_level(logging.WARNING):
        FileSecretStore(tmp_path)
    assert "local development only" in caplog.text


@pytest.mark.parametrize("name", ["", ".", "..", "../etc", "a/b", ".hidden"])
def test_names_cannot_climb_out(tmp_path: Path, name: str) -> None:
    with pytest.raises(ValueError, match="not a plain Secret name"):
        FileSecretStore(tmp_path).put(name, {"token": "x"}, "A")


def test_materialize_writes_read_only_files_and_reports_the_missing(tmp_path: Path) -> None:
    store = FileSecretStore(tmp_path / "store")
    store.put("from-store", {"token": "s"}, "A")
    (tmp_path / "mounted" / "from-mount").mkdir(parents=True)
    (tmp_path / "mounted" / "from-mount" / "token").write_text("m")
    missing = materialize(
        ["from-mount", "from-store", "absent"], tmp_path / "run", store, tmp_path / "mounted"
    )
    assert missing == ["absent"]
    token = tmp_path / "run" / "from-store" / "token"
    assert token.read_text() == "s"
    assert stat.S_IMODE(token.stat().st_mode) == 0o400
    assert stat.S_IMODE((tmp_path / "run").stat().st_mode) == 0o700
    assert (tmp_path / "run" / "from-mount" / "token").read_text() == "m"


def test_secret_names_in_finds_every_reference() -> None:
    spec = {"target": {"auth": {"secretRef": {"name": "b"}}, "x": [{"secretRef": {"name": "a"}}]}}
    assert secret_names_in(spec) == ["a", "b"]


def test_secret_names_in_finds_a_tls_hops_ca_secret() -> None:
    spec = {
        "target": {"tls": {"enabled": True, "caSecretRef": {"name": "vllm-ca"}}},
        "prometheus": {"tls": {"enabled": False}},
    }
    assert secret_names_in(spec) == ["vllm-ca"]


class FakeClient:
    """Records lightkube calls; Secrets live in a dict."""

    def __init__(self) -> None:
        self.secrets: dict[str, Any] = {}
        self.applied: list[tuple[Any, dict[str, Any]]] = []

    def apply(self, secret: Any, **options: Any) -> None:
        self.applied.append((secret, options))
        self.secrets[secret.metadata.name] = secret

    def get(self, _kind: object, name: str) -> Any:
        if name not in self.secrets:
            raise _not_found()
        return self.secrets[name]

    def delete(self, _kind: object, name: str) -> None:
        if name not in self.secrets:
            raise _not_found()
        del self.secrets[name]


def _not_found() -> ApiError:
    error = ApiError.__new__(ApiError)
    error.status = SimpleNamespace(code=404)  # type: ignore[assignment]
    return error


def test_kubernetes_store_applies_labelled_secrets_and_reads_them_back() -> None:
    client = FakeClient()
    store = KubernetesSecretStore("servtuner", client=client)  # type: ignore[arg-type]
    store.put("servtuner-target-01abc-api-key", {"token": "sk-live"}, "01ABC")

    secret, options = client.applied[0]
    assert options == {"field_manager": "servtuner-api", "force": True}
    assert secret.metadata.labels == {
        "app.kubernetes.io/managed-by": "servtuner",
        "servtuner.dev/target-id": "01abc",
    }
    assert secret.data == {"token": base64.b64encode(b"sk-live").decode()}
    assert secret.stringData is None, "data, so server-side apply owns and prunes each key"
    assert store.read("servtuner-target-01abc-api-key") == {"token": "sk-live"}
    assert store.read("absent") is None
    store.delete("servtuner-target-01abc-api-key")
    store.delete("servtuner-target-01abc-api-key")
    assert store.read("servtuner-target-01abc-api-key") is None
