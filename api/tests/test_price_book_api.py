"""The install's price book: kept once, copied into new runs as
analysis.infra_pricing, and never into a host target's runs, which their host prices."""

import copy
import json
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.models.validation import validate
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.test_managed_targets_api import QWEN, TUNING_NAMESPACES

STATIC_BOOK: dict[str, Any] = {
    "source": "static",
    "discount": 0.7,
    "nodes": {"p5en.48xlarge": {"usd_per_hour": 63.3, "cores": 188, "memory_gib": 1900, "gpus": 8}},
    "gpus": {"NVIDIA H200": {"usd_per_hour": 7.9}},
}
STATIC_PRICING = {
    "source": "static",
    "discount": 0.7,
    "static": {
        "nodes": {
            "p5en.48xlarge": {"usd_per_hour": 63.3, "cores": 188, "memory_gib": 1900, "gpus": 8}
        },
        "gpus": {"NVIDIA H200": {"usd_per_hour": 7.9}},
    },
}
RUNSPEC_EXAMPLE = (
    Path(__file__).parents[2] / "schema/examples/runspec/valid/infra-pricing-static.json"
)


@pytest.fixture
def client(engine: Engine, data_dir: Path) -> Iterator[TestClient]:
    def service_with_namespaces():  # type: ignore[no-untyped-def]
        service = build_service(engine, FakeRunner(), data_dir)
        service.managed_targets = ManagedTargetService(
            service._repository,  # noqa: SLF001 - the test assembles the service by hand
            TUNING_NAMESPACES,
        )
        return service

    with TestClient(create_app(service_with_namespaces)) as test_client:
        yield test_client


def test_an_empty_install_prices_nothing(client: TestClient) -> None:
    assert client.get("/price-book").json() == {
        "source": "none",
        "discount": 1,
        "nodes": {},
        "gpus": {},
        "offline": False,
    }
    assert "infra_pricing" not in client.get("/run-defaults").json()


def test_a_static_book_becomes_new_runs_infra_pricing(client: TestClient) -> None:
    saved = client.put("/price-book", json=STATIC_BOOK)
    assert saved.status_code == 200, saved.text
    assert saved.json()["infra_pricing"] == STATIC_PRICING
    assert saved.json()["updated_at"]
    assert client.get("/run-defaults").json()["infra_pricing"] == STATIC_PRICING

    # The block is a valid RunSpec analysis.infra_pricing.
    spec = json.loads(RUNSPEC_EXAMPLE.read_text())
    spec["analysis"] = {"infra_pricing": STATIC_PRICING}
    validate("runspec", spec)


def test_an_aws_book_names_its_region(client: TestClient) -> None:
    saved = client.put("/price-book", json={"source": "aws", "aws_region": "eu-central-1"})
    assert saved.status_code == 200, saved.text
    assert saved.json()["infra_pricing"] == {"source": "aws", "aws": {"region": "eu-central-1"}}
    # Switching back to none keeps the tables but prices nothing.
    off = client.put("/price-book", json={**STATIC_BOOK, "source": "none"})
    assert "infra_pricing" not in off.json()
    assert off.json()["nodes"] == STATIC_BOOK["nodes"]
    assert "infra_pricing" not in client.get("/run-defaults").json()


@pytest.mark.parametrize(
    ("body", "problem"),
    [
        ({"source": "static"}, "at least one node type or GPU model"),
        ({"source": "aws"}, "aws_region"),
        ({"source": "aws", "aws_region": "Frankfurt"}, "aws_region"),
        ({**STATIC_BOOK, "discount": 0}, "discount"),
        (
            {**STATIC_BOOK, "nodes": {"m5": {"usd_per_hour": 1, "cores": 0, "memory_gib": 8}}},
            "cores",
        ),
        ({**STATIC_BOOK, "gpus": {"": {"usd_per_hour": 1}}}, "gpus"),
        ({**STATIC_BOOK, "source": "datasheet"}, "source"),
    ],
)
def test_refuses_a_book_that_cannot_price(
    client: TestClient, body: dict[str, Any], problem: str
) -> None:
    refused = client.put("/price-book", json=body)
    assert refused.status_code == 422
    assert problem in refused.text
    assert client.get("/price-book").json()["source"] == "none"


def test_a_kubernetes_target_is_priced_from_the_book(client: TestClient) -> None:
    created = client.post("/managed-targets", json=copy.deepcopy(QWEN)).json()
    assert created.get("run_analysis") is None
    assert not any("cost" in name for name in created["session_objectives"])

    client.put("/price-book", json=STATIC_BOOK)
    priced = client.get(f"/managed-targets/{created['id']}").json()
    assert priced["run_analysis"] == {"infra_pricing": STATIC_PRICING}
    assert priced["session_objectives"][-2:] == [
        "infra_cost_per_1k_requests",
        "infra_cost_per_1m_output_tokens",
    ]
