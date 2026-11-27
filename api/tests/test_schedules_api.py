"""Scheduled regression runs: slots, overlap, labels and verdicts (api/schedules.py)."""

import copy
import json
from collections.abc import Iterator
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.database import create_session_factory
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.records.repository import RunRepository
from servtuner_api.runner import RunnerState
from servtuner_api.schedules import (
    SCHEDULE_LABEL,
    Measurement,
    Verdict,
    first_slot,
    next_slot,
    verdict,
)
from servtuner_api.service import RunService
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes
from tests.support.waiting import wait_until

SPEC = json.loads(run_spec_bytes())
ANALYSIS = json.loads(example_bytes("analysis", "valid", "degraded-with-capacity"))
T0 = datetime(2026, 11, 1, 2, 0, tzinfo=UTC)
HOUR = 3600
DAY = 24 * HOUR


def test_next_slot_skips_missed_slots_instead_of_replaying_them() -> None:
    assert next_slot(T0, DAY, T0) == T0 + timedelta(days=1)
    assert next_slot(T0, DAY, T0 + timedelta(days=3, hours=5)) == T0 + timedelta(days=4)
    assert first_slot(T0 + timedelta(hours=1), DAY, T0) == T0 + timedelta(hours=1)
    assert first_slot(T0, DAY, T0 + timedelta(hours=1)) == T0 + timedelta(days=1)


@pytest.mark.parametrize(
    ("rps", "expected"),
    [
        (100.0, Verdict.STEADY),
        (91.0, Verdict.STEADY),
        (89.0, Verdict.REGRESSED),
        (111.0, Verdict.IMPROVED),
    ],
)
def test_a_run_is_judged_by_its_slo_max_against_the_baseline(rps: float, expected: Verdict) -> None:
    judged, change = verdict("succeeded", True, Measurement(rps, False), 100.0, False, 0.10)
    assert judged is expected
    assert change == pytest.approx(rps / 100 - 1)


def test_runs_without_a_measurement_are_not_judged() -> None:
    usable = Measurement(100.0, False)
    assert verdict("running", False, usable, 100.0, False, 0.1)[0] is Verdict.PENDING
    assert verdict("failed", True, usable, 100.0, False, 0.1)[0] is Verdict.FAILED
    assert (
        verdict("succeeded", True, Measurement(None, False), 100.0, False, 0.1)[0]
        is Verdict.UNMEASURED
    )
    assert (
        verdict("succeeded", True, Measurement(90.0, True), 100.0, False, 0.1)[0]
        is Verdict.UNMEASURED
    )
    assert verdict("succeeded", True, usable, None, False, 0.1)[0] is Verdict.NO_BASELINE
    assert verdict("succeeded", True, usable, 100.0, True, 0.1)[0] is Verdict.BASELINE


@pytest.fixture
def service(engine: Engine, fake_runner: FakeRunner, data_dir: Path) -> Iterator[RunService]:
    """A second service over the same database, driven with explicit clocks."""
    service = build_service(engine, fake_runner, data_dir)
    yield service
    service.stop()


def create(client: TestClient, **changes: object) -> dict[str, Any]:
    body = {"name": "nightly vllm", "spec": SPEC, "every_hours": 24} | changes
    response = client.post("/schedules", json=body)
    assert response.status_code == 201, response.text
    return response.json()


def finish(
    service: RunService, runner: FakeRunner, data_dir: Path, run_id: str, rps: float | None
) -> None:
    """End the run as succeeded with an analysis whose SLO-max is ``rps`` (none when None)."""
    analysis = copy.deepcopy(ANALYSIS)
    if rps is None:
        analysis["capacity"].pop("slo_max", None)
    else:
        analysis["capacity"]["slo_max"]["rps"] = rps
    analysis["quality"]["is_excluded_from_comparison"] = False
    bundle = run_bundle_dir(data_dir, run_id)
    bundle.mkdir(parents=True, exist_ok=True)
    (bundle / "analysis.json").write_text(json.dumps(analysis))
    runner.finish(run_id, RunnerState.SUCCEEDED, exit_code=0)
    run = service.get_run(run_id)
    assert run is not None
    service.index_metrics(run)


def test_a_due_schedule_launches_a_labelled_run_and_never_overlaps(
    client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path, service: RunService
) -> None:
    schedule = create(client, start_at=T0.isoformat())
    assert schedule["next_run_at"].startswith("2026-11-01T02:00"), "a past start is due at once"

    (first,) = service.schedules.run_due(T0 + timedelta(minutes=1))
    labels = json.loads(fake_runner.requests[first].spec_raw)["labels"]
    assert labels[SCHEDULE_LABEL] == schedule["id"]
    assert service.schedules.run_due(T0 + timedelta(minutes=2)) == [], "not due again yet"

    # The next slot falls due while the first run is still going: it is skipped.
    assert service.schedules.run_due(T0 + timedelta(days=1, minutes=1)) == []
    detail = client.get(f"/schedules/{schedule['id']}").json()
    assert first in detail["last_note"] and "skipped" in detail["last_note"]
    assert detail["next_run_at"].startswith("2026-11-03T02:00")
    assert client.post(f"/schedules/{schedule['id']}/trigger").status_code == 409

    finish(service, fake_runner, data_dir, first, 100.0)
    wait_until(lambda: client.get(f"/runs/{first}").json()["status"] == "succeeded", "final")
    (second,) = service.schedules.run_due(T0 + timedelta(days=2, minutes=1))
    assert client.get(f"/schedules/{schedule['id']}").json()["last_run_id"] == second


def test_the_history_flags_a_regression_against_the_first_measured_run(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, service: RunService
) -> None:
    schedule = create(client, start_at=T0.isoformat())
    measured = [None, 100.0, 95.0, 80.0, 120.0]
    run_ids = []
    for day, rps in enumerate(measured):
        (run_id,) = service.schedules.run_due(T0 + timedelta(days=day, minutes=1))
        finish(service, fake_runner, data_dir, run_id, rps)
        wait_until(lambda r=run_id: service.get_run(r).status == "succeeded", "final")  # type: ignore[union-attr]
        run_ids.append(run_id)

    detail = client.get(f"/schedules/{schedule['id']}").json()
    assert detail["effective_baseline_run_id"] == run_ids[1], "the first run with an SLO-max"
    assert detail["baseline_slo_max_rps"] == 100.0
    # The runs list carries the same SLO-max (absent for the run that has none).
    listed = {run["id"]: run.get("slo_max_rps") for run in client.get("/runs").json()}
    assert [listed[run_id] for run_id in run_ids] == measured
    assert client.get(f"/runs/{run_ids[1]}").json()["slo_max_rps"] == 100.0
    verdicts = [run["verdict"] for run in detail["runs"]]
    assert verdicts == ["unmeasured", "baseline", "steady", "regressed", "improved"]
    assert detail["runs"][3]["change"] == pytest.approx(-0.2)

    # An explicit baseline replaces the first measured run.
    response = client.patch(f"/schedules/{schedule['id']}", json={"baseline_run_id": run_ids[3]})
    assert response.status_code == 200, response.text
    detail = client.get(f"/schedules/{schedule['id']}").json()
    assert [run["verdict"] for run in detail["runs"]][1:] == [
        "improved",
        "improved",
        "baseline",
        "improved",
    ]


def test_disabled_schedules_are_not_launched_and_triggering_launches_now(
    client: TestClient, fake_runner: FakeRunner, service: RunService
) -> None:
    schedule = create(client, start_at=T0.isoformat(), is_enabled=False)
    assert service.schedules.run_due(T0 + timedelta(days=5)) == []
    response = client.post(f"/schedules/{schedule['id']}/trigger")
    assert response.status_code == 201, response.text
    assert response.json()["labels"][SCHEDULE_LABEL] == schedule["id"]
    assert client.patch(f"/schedules/{schedule['id']}", json={"is_enabled": True}).json()[
        "is_enabled"
    ]


def test_a_future_start_waits_for_its_slot(client: TestClient, service: RunService) -> None:
    start = datetime.now(UTC) + timedelta(hours=3)
    schedule = create(client, start_at=start.isoformat(), every_hours=6)
    assert datetime.fromisoformat(schedule["next_run_at"]) == start
    assert service.schedules.run_due(datetime.now(UTC)) == []


def test_bad_schedules_are_rejected(client: TestClient) -> None:
    body = {"name": "x", "spec": SPEC, "every_hours": 24}
    assert client.post("/schedules", json=body | {"every_hours": 0.5}).status_code == 422
    assert client.post("/schedules", json=body | {"spec": {"name": "broken"}}).status_code == 422
    unknown = body | {"baseline_run_id": "01ARZ3NDEKTSV4RRFFQ69G5FAV"}
    assert client.post("/schedules", json=unknown).status_code == 404
    assert client.get("/schedules/01ARZ3NDEKTSV4RRFFQ69G5FAV").status_code == 404
    assert client.get("/schedules/not-an-id").status_code == 404


def test_schedules_are_listed_and_deleted_keeping_their_runs(
    client: TestClient, engine: Engine, service: RunService
) -> None:
    schedule = create(client, start_at=T0.isoformat())
    (run_id,) = service.schedules.run_due(T0 + timedelta(minutes=1))
    assert [row["id"] for row in client.get("/schedules").json()] == [schedule["id"]]
    assert client.delete(f"/schedules/{schedule['id']}").status_code == 204
    assert client.get("/schedules").json() == []
    assert RunRepository(create_session_factory(engine)).get(run_id) is not None
