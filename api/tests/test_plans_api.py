"""POST /plans: the api fetches the runs' capacity models and hands them to servtuner plan.

A stand-in bench checks the command line and answers with a schema example, so these tests cover
the api's part; bench's planner has its own tests (bench/internal/plan).
"""

import json
import stat
from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import text
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.bundles import FileBundleStore
from servtuner_api.database import create_session_factory
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.planner import Planner
from servtuner_api.records.repository import RunRepository
from servtuner_api.service import RunService
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes

# Answers with the example plan when given two models and the request; with
# PLAN_FAIL set it fails the way bench does for a plan it cannot compute.
FAKE_BENCH = """#!/bin/sh
[ "$1" = "plan" ] || exit 9
[ -n "$PLAN_FAIL" ] && { echo "servtuner: the models measured different workloads" >&2; exit 1; }
echo "$@" > "$(dirname "$0")/argv.txt"
cat "$(dirname "$0")/plan.json"
"""
REQUEST = json.loads(example_bytes("plan-request", "valid", "minimal"))


@pytest.fixture
def bench(tmp_path: Path) -> Path:
    script = tmp_path / "bench"
    script.write_text(FAKE_BENCH)
    script.chmod(script.stat().st_mode | stat.S_IXUSR)
    (tmp_path / "plan.json").write_bytes(example_bytes("plan", "valid", "one-tier"))
    return script


@pytest.fixture
def plan_client(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path, bench: Path
) -> Iterator[TestClient]:
    store = FileBundleStore(data_dir)

    def service() -> RunService:
        return RunService(
            repository=RunRepository(create_session_factory(engine)),
            runner=fake_runner,
            store=store,
            run_token_ttl_seconds=3600,
            runner_poll_seconds=0.02,
            planner=Planner(bench, store),
        )

    with TestClient(create_app(service)) as client:
        yield client


def analyzed_run(client: TestClient, data_dir: Path) -> str:
    response = client.post("/runs", content=run_spec_bytes())
    assert response.status_code == 201
    run_id = str(response.json()["id"])
    bundle = run_bundle_dir(data_dir, run_id)
    bundle.mkdir(parents=True, exist_ok=True)
    (bundle / "capacity_model.json").write_bytes(
        example_bytes("capacity-model", "valid", "bifrost-two-stages")
    )
    return run_id


def test_a_plan_is_made_from_the_runs_capacity_models(
    plan_client: TestClient, data_dir: Path, bench: Path
) -> None:
    run_ids = [analyzed_run(plan_client, data_dir), analyzed_run(plan_client, data_dir)]
    response = plan_client.post("/plans", json={"request": REQUEST, "run_ids": run_ids})
    assert response.status_code == 200, response.text
    assert response.json()["tiers"][0]["key"] == "bifrost"
    argv = (bench.parent / "argv.txt").read_text().split()
    assert argv[:2] == ["plan", "--input"]
    assert len(argv[argv.index("--models") + 1].split(",")) == 2


def test_a_payload_series_is_passed_to_bench(
    plan_client: TestClient, data_dir: Path, bench: Path
) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    series = [analyzed_run(plan_client, data_dir), analyzed_run(plan_client, data_dir)]
    body = {"request": REQUEST, "run_ids": [run_id], "payload_run_ids": series}
    assert plan_client.post("/plans", json=body).status_code == 200
    argv = (bench.parent / "argv.txt").read_text().split()
    assert len(argv[argv.index("--models") + 1].split(",")) == 1
    assert len(argv[argv.index("--payload-models") + 1].split(",")) == 2


def test_without_a_series_bench_gets_no_payload_flag(
    plan_client: TestClient, data_dir: Path, bench: Path
) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    assert (
        plan_client.post("/plans", json={"request": REQUEST, "run_ids": [run_id]}).status_code
        == 200
    )
    assert "--payload-models" not in (bench.parent / "argv.txt").read_text()


@pytest.mark.parametrize(
    ("body", "status"),
    [
        ({"request": {"peak_rps": 1}, "run_ids": ["x"]}, 422),  # request breaks its schema
        ({"request": REQUEST, "run_ids": []}, 422),
        ({"request": REQUEST}, 422),
        ({"request": REQUEST, "run_ids": ["x"], "payload_run_ids": "x"}, 422),
        ({"request": REQUEST, "run_ids": ["x"], "extra": 1}, 422),
        (
            {"request": REQUEST, "run_ids": [], "payload_run_ids": ["01ARZ3NDEKTSV4RRFFQ69G5FAV"]},
            422,
        ),
        ({"request": REQUEST, "run_ids": ["01ARZ3NDEKTSV4RRFFQ69G5FAV"]}, 404),  # unknown run
    ],
)
def test_bad_requests(plan_client: TestClient, body: object, status: int) -> None:
    assert plan_client.post("/plans", json=body).status_code == status


def test_a_run_without_a_capacity_model_is_404(plan_client: TestClient) -> None:
    run_id = plan_client.post("/runs", content=run_spec_bytes()).json()["id"]
    response = plan_client.post("/plans", json={"request": REQUEST, "run_ids": [run_id]})
    assert response.status_code == 404 and "capacity_model.json" in response.text


def test_a_plan_bench_cannot_compute_is_422_with_its_reason(
    plan_client: TestClient, data_dir: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    monkeypatch.setenv("PLAN_FAIL", "1")
    response = plan_client.post("/plans", json={"request": REQUEST, "run_ids": [run_id]})
    assert response.status_code == 422 and "different workloads" in response.text


def test_without_bench_the_planner_is_unavailable(client: TestClient, data_dir: Path) -> None:
    run_id = analyzed_run(client, data_dir)
    assert client.post("/plans", json={"request": REQUEST, "run_ids": [run_id]}).status_code == 503


def test_a_run_in_another_store_is_404(
    plan_client: TestClient, data_dir: Path, engine: Engine
) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    with engine.begin() as connection:
        connection.execute(
            text("UPDATE runs SET bundle_store = 'archive' WHERE id = :id"), {"id": run_id}
        )
    response = plan_client.post("/plans", json={"request": REQUEST, "run_ids": [run_id]})
    assert response.status_code == 404 and "archive" in response.text


def test_a_scenario_is_evaluated_like_a_plan(plan_client: TestClient, data_dir: Path) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    override = {"node": "bifrost.h_rep", "kind": "parameter", "value": 0.1}
    body = {"request": REQUEST | {"overrides": [override]}, "run_ids": [run_id]}
    response = plan_client.post("/scenarios/evaluate", json=body)
    assert response.status_code == 200, response.text
    assert response.json()["tiers"][0]["key"] == "bifrost"
    assert plan_client.post("/scenarios/evaluate", json={"run_ids": [run_id]}).status_code == 422


def test_scenarios_are_saved_listed_loaded_and_deleted(
    plan_client: TestClient, data_dir: Path
) -> None:
    run_id = analyzed_run(plan_client, data_dir)
    scenario = {"request": REQUEST, "run_ids": [run_id]}
    created = plan_client.post(
        "/scenarios", json={"name": " peak 400 ", "note": "N+1", "scenario": scenario}
    )
    assert created.status_code == 201, created.text
    saved = created.json()
    assert saved["name"] == "peak 400" and saved["scenario"]["run_ids"] == [run_id]
    second = plan_client.post("/scenarios", json={"name": "b", "scenario": scenario}).json()
    assert [row["id"] for row in plan_client.get("/scenarios").json()] == [
        second["id"],
        saved["id"],
    ]
    assert plan_client.get(f"/scenarios/{saved['id']}").json() == saved
    assert plan_client.delete(f"/scenarios/{saved['id']}").status_code == 204
    assert plan_client.get(f"/scenarios/{saved['id']}").status_code == 404
    assert plan_client.delete(f"/scenarios/{saved['id']}").status_code == 404


@pytest.mark.parametrize(
    ("body", "status"),
    [
        ({"name": "x"}, 422),
        (
            {
                "name": "",
                "scenario": {"request": REQUEST, "run_ids": ["01J0000000000000000000000X"]},
            },
            422,
        ),
        ({"name": "x", "scenario": {"request": {"peak_rps": 1}, "run_ids": ["a"]}}, 422),
        (
            {
                "name": "x",
                "scenario": {"request": REQUEST, "run_ids": ["01J0000000000000000000000X"]},
            },
            404,
        ),
    ],
)
def test_a_scenario_is_checked_before_it_is_saved(
    plan_client: TestClient, body: object, status: int
) -> None:
    assert plan_client.post("/scenarios", json=body).status_code == status
