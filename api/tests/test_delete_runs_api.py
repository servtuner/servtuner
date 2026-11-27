"""Deleting a run: only on a user's request, refused while something
needs the run, and taking its bundle and the analyses that included it."""

import json
from datetime import UTC, datetime
from pathlib import Path

from fastapi.testclient import TestClient
from sqlalchemy import text
from sqlalchemy.engine import Engine

from servtuner_api.database import (
    ExperimentAnalysisRow,
    ScenarioRow,
    TuningSessionRow,
    create_session_factory,
)
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.records.repository import RunRepository
from servtuner_api.runner import RunnerState
from servtuner_api.tuning.models import SESSION_LABEL
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes
from tests.support.waiting import wait_until

T0 = datetime(2026, 11, 25, 12, 0, tzinfo=UTC)
PLAN_REQUEST = json.loads(example_bytes("plan-request", "valid", "minimal"))
SESSION_ID = "01M8A0000000000000000000SS"


def finished_run(client: TestClient, runner: FakeRunner, data_dir: Path, **changes: object) -> str:
    response = client.post("/runs", content=run_spec_bytes(**changes))
    assert response.status_code == 201, response.text
    run_id = str(response.json()["id"])
    bundle = run_bundle_dir(data_dir, run_id)
    bundle.mkdir(parents=True, exist_ok=True)
    for name in ("manifest.json", "summary.json", "raw/requests.parquet"):
        (bundle / name).parent.mkdir(parents=True, exist_ok=True)
        (bundle / name).write_text("{}")
    runner.finish(run_id, RunnerState.SUCCEEDED, exit_code=0)
    wait_until(lambda: client.get(f"/runs/{run_id}").json()["status"] == "succeeded", "final")
    return run_id


def repository(engine: Engine) -> RunRepository:
    return RunRepository(create_session_factory(engine))


def add_analysis(
    engine: Engine, analysis_id: str, run_ids: list[str], experiment: str = "concurrency"
) -> None:
    repository(engine).insert_analysis(
        ExperimentAnalysisRow(
            id=analysis_id,
            experiment=experiment,
            created_at=T0,
            objective="max_rps",
            run_ids=run_ids,
            comparison={},
        )
    )


def add_session(engine: Engine, status: str, experiment: str = "tune-qwen") -> None:
    repository(engine).insert_tuning_session(
        TuningSessionRow(
            id=SESSION_ID,
            managed_target_id="01M8A0000000000000000000MT",
            status=status,
            request={},
            experiment=experiment,
            boot_results=[],
            history=[],
            runs_started=1,
            created_at=T0,
            updated_at=T0,
        )
    )


def test_a_finished_run_is_deleted_with_its_bundle_and_analyses(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    run_id = finished_run(client, fake_runner, data_dir, experiment="concurrency")
    other = finished_run(client, fake_runner, data_dir, experiment="concurrency")
    add_analysis(engine, "01M8A00000000000000000AN01", [run_id, other])
    add_analysis(engine, "01M8A00000000000000000AN02", [other])

    impact = client.get(f"/runs/{run_id}/delete-impact")
    assert impact.status_code == 200, impact.text
    assert impact.json() == {
        "run_id": run_id,
        "blocks": [],
        "analyses": ["01M8A00000000000000000AN01"],
        "bundle_files": 3,
        "notes": [],
    }
    assert run_bundle_dir(data_dir, run_id).is_dir(), "the impact deletes nothing"

    deleted = client.delete(f"/runs/{run_id}")
    assert deleted.status_code == 200, deleted.text
    assert client.get(f"/runs/{run_id}").status_code == 404
    assert not run_bundle_dir(data_dir, run_id).exists()
    assert run_bundle_dir(data_dir, other).is_dir(), "another run's bundle is untouched"
    with engine.connect() as connection:
        left = connection.execute(text("SELECT id FROM experiment_analyses")).scalars().all()
        events = connection.execute(
            text("SELECT count(*) FROM run_events WHERE run_id = :id"), {"id": run_id}
        ).scalar()
    assert left == ["01M8A00000000000000000AN02"]
    assert events == 0
    assert client.delete(f"/runs/{run_id}").status_code == 404


def test_a_delete_that_lost_its_bundle_still_finishes(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path
) -> None:
    # A delete that removed the bundle and then failed leaves the row; retrying finishes it.
    run_id = finished_run(client, fake_runner, data_dir)
    for file in sorted(run_bundle_dir(data_dir, run_id).rglob("*"), reverse=True):
        file.unlink() if file.is_file() else file.rmdir()
    run_bundle_dir(data_dir, run_id).rmdir()
    assert client.delete(f"/runs/{run_id}").status_code == 200
    assert client.get(f"/runs/{run_id}").status_code == 404


def test_an_unfinished_run_must_be_cancelled_first(client: TestClient) -> None:
    run_id = client.post("/runs", content=run_spec_bytes()).json()["id"]
    wait_until(lambda: client.get(f"/runs/{run_id}").json()["status"] == "running", "running")
    refused = client.delete(f"/runs/{run_id}")
    assert refused.status_code == 409
    assert refused.json()["detail"]["blocks"] == ["it is running: cancel it first"]
    assert client.get(f"/runs/{run_id}").status_code == 200


def test_a_schedule_baseline_and_a_saved_scenario_block_the_delete(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    run_id = finished_run(client, fake_runner, data_dir)
    spec = json.loads(run_spec_bytes())
    schedule = client.post(
        "/schedules",
        json={"name": "nightly", "spec": spec, "every_hours": 24, "baseline_run_id": run_id},
    )
    assert schedule.status_code == 201, schedule.text
    repository(engine).insert_scenario(
        ScenarioRow(
            id="01M8A00000000000000000SC01",
            name="peak 400",
            note=None,
            created_at=T0,
            body={"request": PLAN_REQUEST, "run_ids": [], "payload_run_ids": [run_id]},
        )
    )
    blocks = client.get(f"/runs/{run_id}/delete-impact").json()["blocks"]
    assert blocks == [
        "schedule nightly uses it as its baseline: choose another first",
        "saved scenario peak 400 is built on it: delete the scenario first",
    ]
    refused = client.delete(f"/runs/{run_id}")
    assert refused.status_code == 409 and refused.json()["detail"]["blocks"] == blocks
    assert run_bundle_dir(data_dir, run_id).is_dir(), "a refused delete removes nothing"
    # Freed of both, it goes.
    assert client.delete(f"/schedules/{schedule.json()['id']}").status_code == 204
    assert repository(engine).delete_scenario("01M8A00000000000000000SC01")
    assert client.delete(f"/runs/{run_id}").status_code == 200


def test_a_tuning_sessions_run_waits_for_the_session_to_end(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    run_id = finished_run(client, fake_runner, data_dir, labels={SESSION_LABEL: SESSION_ID.lower()})
    add_session(engine, "running")
    assert client.get(f"/runs/{run_id}/delete-impact").json()["blocks"] == [
        "tuning session tune-qwen is still running it"
    ]
    with engine.begin() as connection:
        connection.execute(text("UPDATE tuning_sessions SET status = 'done'"))
    impact = client.get(f"/runs/{run_id}/delete-impact").json()
    assert impact["blocks"] == []
    assert impact["notes"] == [
        "tuning session tune-qwen keeps its recommendation; its history will show this run as "
        "deleted"
    ]
    assert client.delete(f"/runs/{run_id}").status_code == 200


def add_scenario(engine: Engine, run_id: str) -> None:
    repository(engine).insert_scenario(
        ScenarioRow(
            id="01M8A00000000000000000SC01",
            name="peak 400",
            note=None,
            created_at=T0,
            body={"request": PLAN_REQUEST, "run_ids": [run_id]},
        )
    )


def test_an_experiment_is_deleted_with_its_runs_analyses_and_finished_sessions(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    session_label = {SESSION_LABEL: SESSION_ID.lower()}
    runs = [
        finished_run(client, fake_runner, data_dir, experiment="tune-qwen", labels=session_label)
        for _ in range(2)
    ]
    kept = finished_run(client, fake_runner, data_dir, experiment="concurrency")
    add_analysis(engine, "01M8A00000000000000000AN01", runs, experiment="tune-qwen")
    add_session(engine, "done", experiment="tune-qwen")

    impact = client.get("/experiments/tune-qwen/delete-impact")
    assert impact.status_code == 200, impact.text
    assert impact.json() == {
        "name": "tune-qwen",
        "blocks": [],
        "runs": runs,
        "analyses": 1,
        "bundle_files": 6,
        "sessions": [SESSION_ID],
        "scenarios": [],
        "notes": [],
    }
    deleted = client.delete("/experiments/tune-qwen")
    assert deleted.status_code == 200, deleted.text
    assert client.get("/experiments/tune-qwen").status_code == 404
    for run_id in runs:
        assert client.get(f"/runs/{run_id}").status_code == 404
        assert not run_bundle_dir(data_dir, run_id).exists()
    assert client.get(f"/runs/{kept}").status_code == 200, "another experiment is untouched"
    with engine.connect() as connection:
        assert connection.execute(text("SELECT count(*) FROM tuning_sessions")).scalar() == 0
        assert connection.execute(text("SELECT count(*) FROM experiment_analyses")).scalar() == 0
    assert client.delete("/experiments/tune-qwen").status_code == 404


def test_an_active_session_blocks_its_experiment(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    run_id = finished_run(
        client,
        fake_runner,
        data_dir,
        experiment="tune-qwen",
        labels={SESSION_LABEL: SESSION_ID.lower()},
    )
    add_session(engine, "awaiting_approval", experiment="tune-qwen")
    refused = client.delete("/experiments/tune-qwen")
    assert refused.status_code == 409
    assert refused.json()["detail"]["blocks"] == [
        "tuning session tune-qwen is still running: cancel it first"
    ], "said once, not again for each of its runs"
    assert client.get(f"/runs/{run_id}").status_code == 200


def test_scenarios_on_its_runs_go_only_when_asked(
    client: TestClient, fake_runner: FakeRunner, data_dir: Path, engine: Engine
) -> None:
    run_id = finished_run(client, fake_runner, data_dir, experiment="concurrency")
    add_scenario(engine, run_id)
    impact = client.get("/experiments/concurrency/delete-impact").json()
    assert impact["blocks"] == []
    assert impact["scenarios"] == [{"id": "01M8A00000000000000000SC01", "name": "peak 400"}]
    refused = client.delete("/experiments/concurrency")
    assert refused.status_code == 409
    assert refused.json()["detail"]["blocks"] == [
        "saved scenario peak 400 is built on its runs: delete it with the experiment or first"
    ]
    assert client.delete("/experiments/concurrency?delete_scenarios=true").status_code == 200
    assert repository(engine).list_scenarios() == []
    assert client.get(f"/runs/{run_id}").status_code == 404


def test_an_unfinished_run_blocks_its_experiment(client: TestClient) -> None:
    run_id = client.post("/runs", content=run_spec_bytes(experiment="concurrency")).json()["id"]
    wait_until(lambda: client.get(f"/runs/{run_id}").json()["status"] == "running", "running")
    blocks = client.get("/experiments/concurrency/delete-impact").json()["blocks"]
    assert len(blocks) == 1 and blocks[0].endswith(": it is running: cancel it first")
    assert client.get("/experiments/nope/delete-impact").status_code == 404
