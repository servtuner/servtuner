"""GET /runs/{id}/files and /runs/{id}/files/{path} on the file store."""

from pathlib import Path
from typing import override

from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.bundles import FileBundleStore
from servtuner_api.database import create_session_factory
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.records.repository import RunRepository
from servtuner_api.service import RunService
from tests.support.fake_runner import FakeRunner
from tests.support.specs import run_spec_bytes


def bundle_with_files(client: TestClient, data_dir: Path) -> str:
    run_id = str(client.post("/runs", content=run_spec_bytes()).json()["id"])
    bundle = run_bundle_dir(data_dir, run_id)
    (bundle / "prometheus").mkdir(parents=True)
    (bundle / "summary.json").write_text('{"a": 1}')
    (bundle / "requests.parquet").write_bytes(b"PAR1" * (1 << 19))  # 2 MiB: several chunks
    (bundle / "prometheus" / "queries.json").write_text("{}")
    return run_id


def test_files_are_listed_and_streamed(client: TestClient, data_dir: Path) -> None:
    run_id = bundle_with_files(client, data_dir)
    listing = client.get(f"/runs/{run_id}/files").json()
    assert [entry["path"] for entry in listing] == [
        "prometheus/queries.json",
        "requests.parquet",
        "summary.json",
    ]
    assert listing[1]["size_bytes"] == 4 * (1 << 19)
    summary = client.get(f"/runs/{run_id}/files/summary.json")
    assert summary.status_code == 200 and summary.json() == {"a": 1}
    assert summary.headers["content-type"] == "application/json"
    raw = client.get(f"/runs/{run_id}/files/requests.parquet")
    assert raw.content == b"PAR1" * (1 << 19)
    assert raw.headers["content-type"] == "application/vnd.apache.parquet"
    nested = client.get(f"/runs/{run_id}/files/prometheus/queries.json")
    assert nested.status_code == 200


def test_missing_files_runs_and_escapes(client: TestClient, data_dir: Path) -> None:
    run_id = bundle_with_files(client, data_dir)
    assert client.get(f"/runs/{run_id}/files/missing.json").status_code == 404
    assert client.get("/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/files").status_code == 404
    assert client.get("/runs/not-a-ulid/files/summary.json").status_code == 404
    (data_dir / "secret.txt").write_text("x")
    escape = client.get(f"/runs/{run_id}/files/%2E%2E/%2E%2E/secret.txt")
    assert escape.status_code in (400, 404) and escape.text != "x"


class PresigningStore(FileBundleStore):
    """A file store that presigns, standing in for S3 with storage.s3.presign."""

    @override
    def presign(self, key: str, path: str) -> str | None:
        super().presign(key, path)
        return f"https://store.example.invalid/{key}{path}?X-Amz-Signature=x"


def test_a_presigning_store_redirects(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    def service() -> RunService:
        return RunService(
            repository=RunRepository(create_session_factory(engine)),
            runner=fake_runner,
            store=PresigningStore(data_dir),
            run_token_ttl_seconds=3600,
            runner_poll_seconds=0.02,
        )

    with TestClient(create_app(service)) as client:
        run_id = bundle_with_files(client, data_dir)
        response = client.get(f"/runs/{run_id}/files/summary.json", follow_redirects=False)
    assert response.status_code == 307
    assert response.headers["location"].startswith(
        f"https://store.example.invalid/runs/{run_id}/summary.json"
    )
