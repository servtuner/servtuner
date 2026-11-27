"""How a saved target is paid for: by the hour (its host, or a node type from the
price book) and or by the token (the datasheet, or prices as given); New run copies run_target and
run_analysis into the RunSpec."""

import copy
import json
from pathlib import Path
from typing import Any

from fastapi.testclient import TestClient

from servtuner_api.models.validation import validate
from tests.test_targets_api import VLLM

RTX_HOST: dict[str, Any] = {
    "name": "gpu-box",
    "hardware": {
        "gpus": [{"model": "NVIDIA GeForce RTX 3070", "memory_bytes": 8589934592}],
        "cpu_cores": 16,
    },
    "price": {"kind": "given", "usd_per_hour": 0.12},
}
BOOK = {
    "source": "aws",
    "aws_region": "eu-central-1",
    "discount": 0.5,
    "nodes": {"g6.xlarge": {"usd_per_hour": 0.8, "cores": 4, "memory_gib": 16, "gpus": 1}},
}
RUNSPEC_EXAMPLE = (
    Path(__file__).parents[2] / "schema/examples/runspec/valid/infra-pricing-static.json"
)


def priced(**pricing: Any) -> dict[str, Any]:
    body = copy.deepcopy(VLLM)
    body["pricing"] = pricing
    return body


def assert_valid_run(target: dict[str, Any]) -> None:
    """What New run builds from the target is a valid RunSpec."""
    spec = json.loads(RUNSPEC_EXAMPLE.read_text())
    spec["target"] = target["run_target"]
    spec["analysis"] = target.get("run_analysis") or {}
    validate("runspec", spec)


def test_an_unpriced_target_runs_as_it_is(client: TestClient) -> None:
    created = client.post("/targets", json=VLLM).json()
    assert created["run_target"] == created["target"]
    assert "run_analysis" not in created
    assert "pricing" not in created


def test_a_target_on_a_host_is_priced_by_the_host(client: TestClient) -> None:
    host = client.post("/hosts", json=RTX_HOST).json()
    created = client.post("/targets", json=priced(hourly={"kind": "host", "host_id": host["id"]}))
    assert created.status_code == 201, created.text
    target = created.json()
    assert target["run_target"]["hardware"] == {
        "host": "gpu-box",
        "accelerator": "NVIDIA GeForce RTX 3070",
        "gpu_count": 1,
        "gpu_memory_bytes": 8589934592,
        "cpu_cores": 16,
    }
    assert target["run_analysis"] == {
        "infra_pricing": {
            "source": "static",
            "static": {"gpus": {"NVIDIA GeForce RTX 3070": {"usd_per_hour": 0.12}}},
        }
    }
    assert_valid_run(target)

    # A host that loses its price no longer prices the target, and the target says why.
    client.put(f"/hosts/{host['id']}", json={**RTX_HOST, "price": None})
    again = client.get(f"/targets/{target['id']}").json()
    assert "run_analysis" not in again
    assert again["pricing_problems"] == ["host gpu-box has no price or no GPU"]


def test_a_node_type_from_the_price_book_and_manual_token_prices(client: TestClient) -> None:
    assert client.put("/price-book", json=BOOK).status_code == 200
    body = priced(
        hourly={"kind": "node_type", "node_type": "g6.xlarge", "gpu_count": 1, "gpu_model": "L4"},
        token={"source": "manual", "usd_per_1m_input_tokens": 0.2, "usd_per_1m_output_tokens": 0.8},
    )
    target = client.post("/targets", json=body).json()
    assert target["run_target"]["hardware"] == {
        "host": "g6.xlarge",
        "accelerator": "L4",
        "gpu_count": 1,
    }
    # The book's node price, static, with its discount, whatever the book's own source.
    assert target["run_analysis"] == {
        "infra_pricing": {
            "source": "static",
            "discount": 0.5,
            "static": {
                "nodes": {
                    "g6.xlarge": {"usd_per_hour": 0.8, "cores": 4, "memory_gib": 16, "gpus": 1}
                }
            },
        },
        "token_pricing": {
            "source": "manual",
            "manual": {"usd_per_1m_input_tokens": 0.2, "usd_per_1m_output_tokens": 0.8},
        },
    }
    assert_valid_run(target)


def test_a_datasheet_target_keeps_its_url(client: TestClient) -> None:
    body = priced(token={"source": "datasheet", "url": "https://prices.example/datasheet.json"})
    target = client.post("/targets", json=body).json()
    assert target["run_analysis"] == {
        "token_pricing": {"source": "datasheet", "url": "https://prices.example/datasheet.json"}
    }
    assert "hardware" not in target["run_target"]


def test_prices_must_name_what_exists(client: TestClient) -> None:
    missing_host = client.post(
        "/targets", json=priced(hourly={"kind": "host", "host_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"})
    )
    assert missing_host.status_code == 422
    assert "no host" in missing_host.text
    missing_node = client.post(
        "/targets", json=priced(hourly={"kind": "node_type", "node_type": "x", "gpu_count": 1})
    )
    assert missing_node.status_code == 422
    assert "no node type x" in missing_node.text
    no_gpus = client.post(
        "/targets", json=priced(hourly={"kind": "node_type", "node_type": "x", "gpu_count": 0})
    )
    assert no_gpus.status_code == 422
    negative = priced(
        token={"source": "manual", "usd_per_1m_input_tokens": -1, "usd_per_1m_output_tokens": 1}
    )
    assert client.post("/targets", json=negative).status_code == 422
