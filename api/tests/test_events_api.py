"""POST /runs/{id}/events: run-token authentication, schema validation, status effects."""

from pathlib import Path

import httpx2
import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.runner import RunnerState
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import event_body, example_paths, run_spec_bytes
from tests.support.waiting import wait_until


@pytest.fixture
def run(client: TestClient, fake_runner: FakeRunner) -> tuple[str, str]:
    assert client.post("/runs", content=run_spec_bytes()).status_code == 201
    return fake_runner.only_token()


def post_event(client: TestClient, run_id: str, token: str | None, body: bytes) -> httpx2.Response:
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    response: httpx2.Response = client.post(f"/runs/{run_id}/events", content=body, headers=headers)
    return response


def status_of(client: TestClient, run_id: str) -> str:
    status: str = client.get(f"/runs/{run_id}").json()["status"]
    return status


def test_valid_event_is_accepted_and_listed(client: TestClient, run: tuple[str, str]) -> None:
    run_id, token = run
    response = post_event(client, run_id, token, event_body(run_id, 0, phase="warmup"))
    assert response.status_code == 202
    (event,) = client.get(f"/runs/{run_id}/events").json()
    assert (event["sequence"], event["phase"], event["time"]) == (
        0,
        "warmup",
        "2026-10-28T12:05:00Z",
    )


def test_missing_or_wrong_token_is_401(client: TestClient, run: tuple[str, str]) -> None:
    run_id, token = run
    body = event_body(run_id, 0)
    assert post_event(client, run_id, None, body).status_code == 401
    assert post_event(client, run_id, token + "x", body).status_code == 401
    assert client.get(f"/runs/{run_id}/events").json() == []


def test_a_token_is_scoped_to_its_run(client: TestClient, run: tuple[str, str]) -> None:
    first_id, first_token = run
    second_id = client.post("/runs", content=run_spec_bytes()).json()["id"]
    response = post_event(client, second_id, first_token, event_body(second_id, 0))
    assert response.status_code == 401
    unknown = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
    assert post_event(client, unknown, first_token, event_body(unknown, 0)).status_code == 401
    assert first_id != second_id


def test_expired_token_is_401(engine: Engine, fake_runner: FakeRunner, data_dir: Path) -> None:
    app = create_app(lambda: build_service(engine, fake_runner, data_dir, run_token_ttl_seconds=-1))
    with TestClient(app) as expired_client:
        expired_client.post("/runs", content=run_spec_bytes())
        run_id, token = fake_runner.only_token()
        response = post_event(expired_client, run_id, token, event_body(run_id, 0))
        assert response.status_code == 401


def test_invalid_events_are_422(client: TestClient, run: tuple[str, str]) -> None:
    run_id, token = run
    for path in example_paths("events", "invalid"):
        response = post_event(client, run_id, token, path.read_bytes())
        assert response.status_code == 422, path.name
    mismatched = post_event(client, run_id, token, event_body("another-run", 0))
    assert mismatched.status_code == 422
    impossible_time = event_body(run_id, 0, time="2026-02-30T00:00:00Z")
    assert post_event(client, run_id, token, impossible_time).status_code == 422


def test_duplicate_sequence_is_accepted_once(client: TestClient, run: tuple[str, str]) -> None:
    run_id, token = run
    for _ in range(2):
        assert post_event(client, run_id, token, event_body(run_id, 0)).status_code == 202
    assert len(client.get(f"/runs/{run_id}/events").json()) == 1


def test_events_drive_running_and_collecting(client: TestClient, run: tuple[str, str]) -> None:
    run_id, token = run
    post_event(client, run_id, token, event_body(run_id, 0, phase="stage-0"))
    detail = client.get(f"/runs/{run_id}").json()
    assert (detail["status"], detail["phase"]) == ("running", "stage-0")
    post_event(client, run_id, token, event_body(run_id, 1, state="collecting", step="collect"))
    assert status_of(client, run_id) == "collecting"
    # A late, lower-sequence event is recorded but must not roll the status back.
    post_event(client, run_id, token, event_body(run_id, 0, phase="late"))
    assert status_of(client, run_id) == "collecting"


def test_bench_reporting_success_does_not_end_the_run(
    client: TestClient, fake_runner: FakeRunner, run: tuple[str, str]
) -> None:
    run_id, token = run
    post_event(client, run_id, token, event_body(run_id, 0, state="succeeded", step="finalize"))
    assert status_of(client, run_id) != "succeeded"
    # bench said succeeded, yet the process died: the runner's word is final.
    fake_runner.finish(run_id, RunnerState.FAILED, exit_code=1)
    wait_until(lambda: status_of(client, run_id) == "failed", "runner status to win")
    post_event(client, run_id, token, event_body(run_id, 1, state="running"))
    assert status_of(client, run_id) == "failed"
