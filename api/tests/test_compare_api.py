"""GET /compare and GET /experiments[/{name}]: runs lined up from run_metrics."""

import copy
import json
from typing import Any

from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.database import create_session_factory
from servtuner_api.indexing import metric_rows
from servtuner_api.records.repository import RunRepository
from tests.support.specs import example_bytes, run_spec_bytes

ANALYSIS: dict[str, Any] = json.loads(example_bytes("analysis", "valid", "degraded-with-capacity"))
UNKNOWN_RUN = "01ARZ3NDEKTSV4RRFFQ69G5FAV"


def analysis_with(slo_max_rps: float, offered_scale: float = 1.0) -> dict[str, Any]:
    document = copy.deepcopy(ANALYSIS)
    document["capacity"]["slo_max"]["rps"] = slo_max_rps
    for stage in document["stages"]:
        stage["offered_rps"] *= offered_scale
    return document


def indexed_run(
    client: TestClient,
    engine: Engine,
    analysis: dict[str, Any] | None,
    knobs: dict[str, Any],
    experiment: str | None = "concurrency",
) -> str:
    """A run created through the api, indexed directly (indexing has its own tests)."""
    body = run_spec_bytes(knobs=knobs, **({"experiment": experiment} if experiment else {}))
    response = client.post("/runs", content=body)
    assert response.status_code == 201, response.text
    run_id = str(response.json()["id"])
    if analysis is not None:
        RunRepository(create_session_factory(engine)).replace_metrics(run_id, metric_rows(analysis))
    return run_id


def cell(comparison: dict[str, Any], stage: str, metric: str, stat: str = "value") -> list[Any]:
    for entry in comparison["metrics"]:
        if (entry["stage"], entry["metric"], entry["stat"]) == (stage, metric, stat):
            return list(entry["values"])
    raise AssertionError(f"no cell {stage}/{metric}/{stat}")


def test_runs_are_aligned_in_the_order_asked(client: TestClient, engine: Engine) -> None:
    low = indexed_run(client, engine, analysis_with(40), {"bifrost.concurrency": 100})
    high = indexed_run(client, engine, analysis_with(55), {"bifrost.concurrency": 400})
    response = client.get(f"/compare?runs={high},{low}")
    assert response.status_code == 200, response.text
    comparison = response.json()
    assert [run["id"] for run in comparison["runs"]] == [high, low]
    assert cell(comparison, "run", "slo_max_rps") == [55, 40]
    assert cell(comparison, "0", "ttft_seconds", "p95") == [0.4, 0.4]
    assert comparison["knobs"] == [
        {"name": "bifrost.concurrency", "values": [400, 100], "differs": True}
    ]
    assert comparison["ladders_match"] is True
    assert comparison["unindexed_run_ids"] == []
    assert comparison["metrics"][0]["stage"] == "run", "run-level values come first"


def test_different_ladders_and_unindexed_runs_are_reported(
    client: TestClient, engine: Engine
) -> None:
    base = indexed_run(client, engine, analysis_with(40), {"a": 1})
    doubled = indexed_run(client, engine, analysis_with(40, offered_scale=2), {"a": 1})
    pending = indexed_run(client, engine, None, {"a": 1, "b": "x"})
    comparison = client.get(f"/compare?runs={base},{doubled},{pending}").json()
    assert comparison["ladders_match"] is False
    assert comparison["unindexed_run_ids"] == [pending]
    assert cell(comparison, "run", "slo_max_rps") == [40, 40, None]
    knobs = {knob["name"]: knob for knob in comparison["knobs"]}
    assert knobs["a"]["differs"] is False
    assert knobs["b"] == {"name": "b", "values": [None, None, "x"], "differs": True}


def test_bad_compare_requests(client: TestClient, engine: Engine) -> None:
    run_id = indexed_run(client, engine, analysis_with(40), {})
    assert client.get(f"/compare?runs={run_id},{UNKNOWN_RUN}").status_code == 404
    assert client.get("/compare?runs=not-a-ulid").status_code == 404
    assert client.get(f"/compare?runs={run_id},{run_id}").status_code == 422
    assert client.get("/compare?runs=,").status_code == 422
    assert client.get("/compare").status_code == 422
    too_many = ",".join([UNKNOWN_RUN] * 51)
    assert client.get(f"/compare?runs={too_many}").status_code == 422


def test_experiments_are_listed_with_their_run_counts(client: TestClient, engine: Engine) -> None:
    indexed_run(client, engine, analysis_with(40), {"k": 1}, experiment="first")
    indexed_run(client, engine, analysis_with(40), {"k": 2}, experiment="second")
    indexed_run(client, engine, analysis_with(40), {"k": 3}, experiment="second")
    indexed_run(client, engine, analysis_with(40), {"k": 4}, experiment=None)
    listing = {entry["name"]: entry for entry in client.get("/experiments").json()}
    assert set(listing) == {"first", "second"}
    assert listing["second"]["run_count"] == 2
    assert listing["first"]["run_count"] == 1
    assert "latest_run_at" in listing["first"]


def test_an_experiment_shows_its_runs_and_the_knobs_it_varies(
    client: TestClient, engine: Engine
) -> None:
    first = indexed_run(client, engine, analysis_with(40), {"c": 100, "fixed": True})
    second = indexed_run(client, engine, analysis_with(50), {"c": 200, "fixed": True})
    response = client.get("/experiments/concurrency")
    assert response.status_code == 200, response.text
    detail = response.json()
    assert detail["run_count"] == 2
    assert detail["varied_knobs"] == ["c"]
    assert [run["id"] for run in detail["comparison"]["runs"]] == [first, second]
    assert cell(detail["comparison"], "run", "slo_max_rps") == [40, 50]
    assert client.get("/experiments/missing").status_code == 404
