"""run_metrics: a finished run's analysis.json, indexed long-format (api/CLAUDE.md)."""

import json
from pathlib import Path

from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.database import create_session_factory
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.indexing import RUN_STAGE, VALUE, MetricRow, metric_rows
from servtuner_api.records.repository import RunRepository
from servtuner_api.runner import RunnerState
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes
from tests.support.waiting import wait_until

DEGRADED = json.loads(example_bytes("analysis", "valid", "degraded-with-capacity"))
UNUSABLE = json.loads(example_bytes("analysis", "valid", "unusable-no-capacity"))


def by_key(rows: list[MetricRow]) -> dict[tuple[str, str, str], float]:
    keyed = {(row.stage, row.metric, row.stat): row.value for row in rows}
    assert len(keyed) == len(rows), "every (stage, metric, stat) is indexed once"
    return keyed


def test_stage_numbers_latencies_and_flags_are_indexed() -> None:
    rows = by_key(metric_rows(DEGRADED))
    assert rows["0", "offered_rps", VALUE] == 5
    assert rows["0", "requests", VALUE] == 600
    assert rows["0", "error_rate", VALUE] == 0
    assert rows["0", "ttft_seconds", "p95"] == 0.4
    p95 = DEGRADED["stages"][0]["ttft"]["p95_seconds"]
    assert rows["0", "ttft_seconds", "p95_ci_low"] == p95["ci_low"]
    assert rows["0", "ttft_seconds", "mean"] == 0.2
    assert ("0", "ttft_seconds", "p99") not in rows, "a percentile with too few samples has no row"
    assert rows["2", "is_client_bound", VALUE] == 1.0
    assert rows["0", "is_valid", VALUE] == 1.0


def test_run_level_capacity_is_indexed_under_the_run_stage() -> None:
    rows = by_key(metric_rows(DEGRADED))
    capacity = DEGRADED["capacity"]
    assert rows[RUN_STAGE, "slo_max_rps", VALUE] == capacity["slo_max"]["rps"]
    assert rows[RUN_STAGE, "is_excluded_from_comparison", VALUE] == 1.0
    per_replica = capacity["per_replica"]
    assert rows[RUN_STAGE, "rps_per_replica", VALUE] == per_replica["rps_per_replica"]


def test_priced_costs_are_indexed_and_an_unpriced_run_gets_no_cost_row() -> None:
    capacity = json.loads(example_bytes("capacity", "valid", "gpus-and-cost"))
    rows = by_key(metric_rows(DEGRADED, capacity))
    assert rows[(RUN_STAGE, "infra_usd_per_replica_hour", VALUE)] == 1.4
    assert rows[(RUN_STAGE, "infra_usd_per_1k_requests", VALUE)] == 0.00778
    assert rows[(RUN_STAGE, "infra_usd_per_1m_output_tokens", VALUE)] == 0.0778
    assert rows[(RUN_STAGE, "token_usd_per_1k_requests", VALUE)] == 0.5
    assert rows[(RUN_STAGE, "token_usd_per_1m_output_tokens", VALUE)] == 4
    unpriced = {
        **capacity,
        "infra_cost": {"source": "static", "is_priced": False, "reason": "no price"},
        "token_cost": {**capacity["token_cost"], "is_priced": False},
    }
    names = {row.metric for row in metric_rows(DEGRADED, unpriced)}
    assert not {name for name in names if "usd" in name}, "unpriced is never shown as $0"
    assert by_key(metric_rows(DEGRADED)) == by_key(metric_rows(DEGRADED, {})), "no capacity.json"


def test_an_analysis_without_capacity_still_indexes_its_quality() -> None:
    rows = by_key(metric_rows(UNUSABLE))
    assert (RUN_STAGE, "slo_max_rps", VALUE) not in rows
    assert (RUN_STAGE, "is_excluded_from_comparison", VALUE) in rows


def write_analysis(data_dir: Path, run_id: str, document: object) -> None:
    bundle = run_bundle_dir(data_dir, run_id)
    bundle.mkdir(parents=True, exist_ok=True)
    (bundle / "analysis.json").write_text(json.dumps(document))


def metrics_of(engine: Engine, run_id: str) -> dict[tuple[str, str, str], float]:
    repository = RunRepository(create_session_factory(engine))
    return {
        (row.stage, row.metric, row.stat): row.value for row in repository.list_metrics([run_id])
    }


def test_a_finished_run_is_indexed(
    client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    run_id = str(client.post("/runs", content=run_spec_bytes()).json()["id"])
    write_analysis(data_dir, run_id, DEGRADED)
    assert metrics_of(engine, run_id) == {}
    fake_runner.finish(run_id, RunnerState.SUCCEEDED, exit_code=0)
    wait_until(lambda: bool(metrics_of(engine, run_id)), "the run's metrics")
    assert metrics_of(engine, run_id) == by_key(metric_rows(DEGRADED))


def test_a_run_without_a_valid_analysis_gets_no_rows_and_still_finishes(
    client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    run_id = str(client.post("/runs", content=run_spec_bytes()).json()["id"])
    write_analysis(data_dir, run_id, {"not": "an analysis"})
    fake_runner.finish(run_id, RunnerState.FAILED, exit_code=1)
    wait_until(lambda: client.get(f"/runs/{run_id}").json()["status"] == "failed", "failed")
    assert metrics_of(engine, run_id) == {}


def test_reindexing_replaces_the_rows(
    client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    service = build_service(engine, fake_runner, data_dir)
    run_id = str(client.post("/runs", content=run_spec_bytes()).json()["id"])
    write_analysis(data_dir, run_id, DEGRADED)
    run = service.get_run(run_id)
    assert run is not None
    assert service.index_metrics(run) == len(metric_rows(DEGRADED))
    write_analysis(data_dir, run_id, UNUSABLE)
    service.index_metrics(run)
    assert metrics_of(engine, run_id) == by_key(metric_rows(UNUSABLE))


def test_startup_backfills_finished_runs_that_have_no_rows(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    app = create_app(lambda: build_service(engine, fake_runner, data_dir))
    with TestClient(app) as first:
        run_id = str(first.post("/runs", content=run_spec_bytes()).json()["id"])
        fake_runner.finish(run_id, RunnerState.SUCCEEDED, exit_code=0)
        wait_until(lambda: first.get(f"/runs/{run_id}").json()["status"] == "succeeded", "done")
    assert metrics_of(engine, run_id) == {}, "no analysis.json yet, so nothing was indexed"
    write_analysis(data_dir, run_id, DEGRADED)
    with TestClient(create_app(lambda: build_service(engine, fake_runner, data_dir))):
        wait_until(lambda: bool(metrics_of(engine, run_id)), "the backfill")
    assert metrics_of(engine, run_id) == by_key(metric_rows(DEGRADED))
