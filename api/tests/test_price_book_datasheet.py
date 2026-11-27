"""The token-price datasheet an operator supplies to the price book, and offline
installs: an upload the api serves to a run's bench with its run token, or an internal URL;
AWS refused offline; GET /run-defaults says both."""

import hashlib
import json
from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.service import RunService
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import run_spec_bytes

SHEET = json.dumps(
    {
        "gpt-4o-mini": {
            "provider": "openai",
            "input_cost_per_token": 1.5e-07,
            "output_cost_per_token": 6e-07,
        },
        "an-embedding-without-token-prices": {"provider": "openai"},
    }
).encode()


def make_client(service: RunService) -> TestClient:
    return TestClient(create_app(lambda: service))


@pytest.fixture
def service(engine: Engine, fake_runner: FakeRunner, data_dir: Path) -> RunService:
    return build_service(engine, fake_runner, data_dir)


@pytest.fixture
def client(service: RunService) -> Iterator[TestClient]:
    with make_client(service) as test_client:
        yield test_client


def test_an_upload_is_kept_byte_for_byte_and_priced_runs_fetch_it_from_the_api(
    client: TestClient,
) -> None:
    book = client.put("/price-book/datasheet", content=SHEET).json()
    assert book["datasheet"] == {
        "source": "upload",
        "sha256": hashlib.sha256(SHEET).hexdigest(),
        "size_bytes": len(SHEET),
        "entries": 2,
        "priced_entries": 1,
        "uploaded_at": book["datasheet"]["uploaded_at"],
    }
    assert book["token_pricing"] == {"source": "datasheet", "from_price_book": True}
    assert client.get("/price-book/datasheet").content == SHEET
    assert client.get("/run-defaults").json()["token_pricing"] == book["token_pricing"]
    assert client.delete("/price-book/datasheet").status_code == 204
    assert "datasheet" not in client.get("/price-book").json()
    assert client.get("/price-book/datasheet").status_code == 404


@pytest.mark.parametrize(
    ("body", "problem"),
    [
        (b"not json", "not JSON"),
        (b"[]", "a JSON object of models"),
        (b'{"m": 1}', "m: not an object"),
        (b'{"m": {"input_cost_per_token": "0.1"}}', "m: a token cost is not a number"),
        (b'{"m": {"provider": "x"}}', "prices no model"),
    ],
)
def test_refuses_a_datasheet_bench_cannot_read(
    client: TestClient, body: bytes, problem: str
) -> None:
    response = client.put("/price-book/datasheet", content=body)
    assert response.status_code == 422
    assert problem in json.dumps(response.json())


def test_a_url_or_an_upload_never_both(client: TestClient) -> None:
    url = "https://prices.example.internal/datasheet.json"
    book = client.put("/price-book", json={"datasheet_url": url}).json()
    assert book["datasheet"] == {"source": "url", "url": url}
    assert book["token_pricing"] == {"source": "datasheet", "url": url}
    assert client.put("/price-book/datasheet", content=SHEET).status_code == 422
    client.put("/price-book", json={})
    assert client.put("/price-book/datasheet", content=SHEET).status_code == 200
    assert client.put("/price-book", json={"datasheet_url": url}).status_code == 422


def test_a_run_token_reads_the_datasheet_on_its_own_run_only(
    client: TestClient, fake_runner: FakeRunner
) -> None:
    assert client.post("/runs", content=run_spec_bytes()).status_code == 201
    run_id, token = fake_runner.only_token()
    path = f"/runs/{run_id}/datasheet"
    assert client.get(path, headers={"Authorization": f"Bearer {token}"}).status_code == 404
    client.put("/price-book/datasheet", content=SHEET)
    answer = client.get(path, headers={"Authorization": f"Bearer {token}"})
    assert (answer.status_code, answer.content) == (200, SHEET)
    assert client.get(path, headers={"Authorization": "Bearer wrong"}).status_code == 401
    assert client.get(path).status_code == 401


def test_an_offline_install_refuses_aws_and_says_so(service: RunService) -> None:
    service.offline = True
    service.price_book.offline = True
    service.image_registry = "registry.example.internal:5000"
    with make_client(service) as client:
        response = client.put("/price-book", json={"source": "aws", "aws_region": "us-east-1"})
        assert response.status_code == 422 and "offline" in json.dumps(response.json())
        assert client.get("/price-book").json()["offline"] is True
        defaults = client.get("/run-defaults").json()
        assert (defaults["offline"], defaults["image_registry"]) == (
            True,
            "registry.example.internal:5000",
        )


def test_a_datasheet_target_without_a_url_uses_the_books(client: TestClient) -> None:
    target = {
        "name": "router",
        "target": {
            "type": "openai-compatible",
            "endpoint": "http://router.example.internal:8080/v1",
            "model": "gpt-4o-mini",
            "tls": {"mode": "platform"},
        },
        "pricing": {"token": {"source": "datasheet"}},
    }
    created = client.post("/targets", json=target).json()
    assert created["run_analysis"] == {"token_pricing": {"source": "datasheet"}}
    client.put("/price-book/datasheet", content=SHEET)
    again = client.get(f"/targets/{created['id']}").json()
    assert again["run_analysis"] == {
        "token_pricing": {"source": "datasheet", "from_price_book": True}
    }
