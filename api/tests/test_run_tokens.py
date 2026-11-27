"""Run tokens: minted when the job launches, unique to the launch, scoped to the run."""

from datetime import UTC, datetime, timedelta
from pathlib import Path

from fastapi.testclient import TestClient

from tests.support.fake_runner import FakeRunner
from tests.support.specs import event_body, run_spec_bytes
from tests.test_events_api import post_event
from tests.test_followup_api import analysed_run

TOKEN_TTL_SECONDS = 3600  # build_service's default


def run_row_token(client: TestClient, run_id: str) -> tuple[str | None, datetime | None]:
    run = client.app.state.service.get_run(run_id)  # type: ignore[attr-defined]
    return run.token_hash, run.token_expires_at


def test_each_launch_gets_its_own_token(client: TestClient, fake_runner: FakeRunner) -> None:
    first = client.post("/runs", content=run_spec_bytes()).json()["id"]
    second = client.post("/runs", content=run_spec_bytes()).json()["id"]
    assert fake_runner.requests[first].run_token != fake_runner.requests[second].run_token


def test_the_token_lives_from_launch(client: TestClient, fake_runner: FakeRunner) -> None:
    before = datetime.now(UTC)
    run_id = client.post("/runs", content=run_spec_bytes()).json()["id"]
    token_hash, expires_at = run_row_token(client, run_id)
    assert token_hash is not None and expires_at is not None
    assert before + timedelta(seconds=TOKEN_TTL_SECONDS) <= expires_at
    assert expires_at <= datetime.now(UTC) + timedelta(seconds=TOKEN_TTL_SECONDS)


def test_a_run_that_has_not_launched_has_no_token(
    client: TestClient, data_dir: Path, fake_runner: FakeRunner
) -> None:
    source = analysed_run(client, data_dir)
    body = {"recommendation_id": "B1/target", "repeats": 1}
    waiting = client.post(f"/runs/{source}/followup", json=body).json()["runs"][0]["id"]
    assert run_row_token(client, waiting) == (None, None)
    source_token = fake_runner.requests[source].run_token
    assert post_event(client, waiting, source_token, event_body(waiting, 0)).status_code == 401
    assert client.post(f"/runs/{waiting}/confirm-applied").status_code == 200
    token = fake_runner.requests[waiting].run_token
    assert post_event(client, waiting, token, event_body(waiting, 0)).status_code == 202


def test_the_token_works_only_on_its_own_events(
    client: TestClient, fake_runner: FakeRunner
) -> None:
    client.post("/runs", content=run_spec_bytes())
    run_id, token = fake_runner.only_token()
    bearer = {"Authorization": f"Bearer {token}"}
    assert client.get("/runs", headers=bearer).status_code == 200  # auth.mode none: no checks
    forwarded = {**bearer, "X-Forwarded-User": "admin"}
    response = client.post(
        f"/runs/{run_id}/events", content=event_body(run_id, 0), headers=forwarded
    )
    assert response.status_code == 400
