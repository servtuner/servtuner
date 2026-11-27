"""POST/GET /runs against real Postgres, with a fake Runner."""

import hashlib
import json
from pathlib import Path

from fastapi.testclient import TestClient
from sqlalchemy import text
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.runner import RunnerState
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, example_paths, run_spec_bytes
from tests.support.waiting import wait_until


def create(client: TestClient, body: bytes) -> dict[str, object]:
    response = client.post("/runs", content=body, headers={"Content-Type": "application/json"})
    assert response.status_code == 201, response.text
    created: dict[str, object] = response.json()
    return created


def status_of(client: TestClient, run_id: object) -> str:
    status: str = client.get(f"/runs/{run_id}").json()["status"]
    return status


def test_healthz(client: TestClient) -> None:
    assert client.get("/healthz").json() == {"status": "ok"}


def test_create_run_stores_submitted_bytes_verbatim(client: TestClient, engine: Engine) -> None:
    # Odd whitespace and key order would be lost by any parse-and-reserialize.
    body = json.dumps(json.loads(run_spec_bytes()), indent=3, sort_keys=True).encode()
    run = create(client, body)
    with engine.connect() as connection:
        stored = connection.execute(
            text("SELECT spec_raw, spec_hash FROM runs WHERE id = :id"), {"id": run["id"]}
        ).one()
    assert bytes(stored.spec_raw) == body
    assert stored.spec_hash == hashlib.sha256(body).hexdigest() == run["spec_hash"]


def test_create_run_allocates_ulid_and_launches(
    client: TestClient, fake_runner: FakeRunner
) -> None:
    run = create(client, run_spec_bytes())
    assert len(str(run["id"])) == 26
    assert run["target_type"] == "vllm"
    assert fake_runner.requests[str(run["id"])].spec_raw == run_spec_bytes()
    assert run["bundle_store"] == "default"
    assert run["bundle_key"] == f"runs/{run['id']}/"
    assert str(run["bundle_uri"]).startswith("file:///")
    assert str(run["bundle_uri"]).endswith(f"/runs/{run['id']}/")
    wait_until(lambda: status_of(client, run["id"]) == "running", "run to be running")


def test_every_run_says_which_model_it_tested(client: TestClient) -> None:
    run = create(client, run_spec_bytes())
    assert run["model"] == "example/model-8b"
    assert client.get(f"/runs/{run['id']}").json()["model"] == "example/model-8b"
    assert [listed["model"] for listed in client.get("/runs").json()] == ["example/model-8b"]


def test_a_run_names_its_workload_preset(client: TestClient) -> None:
    preset_run = create(
        client, run_spec_bytes(workload={"preset": "chat-short@1", "streaming": True})
    )
    assert preset_run["workload_preset"] == "chat-short@1"
    assert client.get(f"/runs/{preset_run['id']}").json()["workload_preset"] == "chat-short@1"
    assert "workload_preset" not in create(client, run_spec_bytes())


def test_a_run_in_a_store_the_api_lacks_keeps_its_metadata(
    client: TestClient, engine: Engine
) -> None:
    """The row names its store; a store this api lacks is never guessed at."""
    run = create(client, run_spec_bytes())
    with engine.begin() as connection:
        connection.execute(
            text("UPDATE runs SET bundle_store = 'archive' WHERE id = :id"), {"id": run["id"]}
        )
    detail = client.get(f"/runs/{run['id']}")
    assert detail.status_code == 200
    assert detail.json()["bundle_store"] == "archive" and "summary" not in detail.json()


def test_invalid_specs_are_rejected_with_violations(client: TestClient) -> None:
    explicit_null = json.loads(run_spec_bytes())
    explicit_null["experiment"] = None  # pydantic accepts this; the schema forbids it
    for body in (json.dumps(explicit_null).encode(), b"{not json", b"[]"):
        response = client.post("/runs", content=body)
        assert response.status_code == 422, body
        assert response.json()["detail"]["violations"]


def test_every_invalid_example_is_rejected(client: TestClient) -> None:
    for path in example_paths("runspec", "invalid"):
        assert client.post("/runs", content=path.read_bytes()).status_code == 422, path.name


def test_valid_examples_are_accepted(client: TestClient) -> None:
    for name in ("minimal-vllm-mtls", "full-vllm-mtls", "router-bifrost-platform"):
        create(client, example_bytes("runspec", "valid", name))


def test_get_unknown_run_is_404(client: TestClient) -> None:
    assert client.get("/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV").status_code == 404
    assert client.get("/runs/not-a-ulid").status_code == 404


def test_list_filters(client: TestClient) -> None:
    create(client, run_spec_bytes(name="a", experiment="exp-1", labels={"team": "red"}))
    create(client, run_spec_bytes(name="b", experiment="exp-2", labels={"team": "blue"}))
    router = json.loads(example_bytes("runspec", "valid", "router-bifrost-platform"))
    create(client, json.dumps(router).encode())

    def names(query: str) -> list[str]:
        return sorted(run["name"] for run in client.get(f"/runs?{query}").json())

    assert len(names("")) == 3
    assert names("experiment=exp-1") == ["a"]
    assert names("label=team=blue") == ["b"]
    assert names("target_type=vllm") == ["a", "b"]
    assert names(f"target_type={router['target']['type']}") == [router["name"]]
    assert names("status=succeeded") == []
    assert client.get("/runs?status=bogus").status_code == 422
    assert client.get("/runs?label=nokey").status_code == 422


def test_runner_final_status_is_authoritative(client: TestClient, fake_runner: FakeRunner) -> None:
    run = create(client, run_spec_bytes())
    fake_runner.finish(str(run["id"]), RunnerState.FAILED, exit_code=137)
    wait_until(lambda: status_of(client, run["id"]) == "failed", "run to be failed")
    detail = client.get(f"/runs/{run['id']}").json()
    assert detail["exit_code"] == 137
    assert detail["finished_at"] is not None


def test_launch_failure_fails_the_run(client: TestClient, fake_runner: FakeRunner) -> None:
    fake_runner.launch_error = OSError("no such binary")
    run = create(client, run_spec_bytes())
    assert run["status"] == "failed"
    assert "no such binary" in str(client.get(f"/runs/{run['id']}").json()["failure_message"])


def test_run_detail_includes_bundle_summary(client: TestClient, data_dir: Path) -> None:
    run = create(client, run_spec_bytes())
    assert "summary" not in client.get(f"/runs/{run['id']}").json()
    bundle = data_dir / "runs" / str(run["id"])
    bundle.mkdir(parents=True)
    (bundle / "summary.json").write_bytes(example_bytes("summary", "valid", "two-stages"))
    assert len(client.get(f"/runs/{run['id']}").json()["summary"]["stages"]) == 2


def test_cancel_stops_the_run_and_leaves_a_finished_one(
    client: TestClient, fake_runner: FakeRunner
) -> None:
    run = create(client, run_spec_bytes())
    wait_until(lambda: status_of(client, run["id"]) == "running", "run to be running")
    cancelled = client.post(f"/runs/{run['id']}/cancel")
    assert cancelled.status_code == 200, cancelled.text
    assert cancelled.json()["status"] == "cancelled"
    assert fake_runner.cancelled == [run["id"]]
    # The runner's later report does not undo it, and a second cancel changes nothing.
    fake_runner.finish(str(run["id"]), RunnerState.FAILED, exit_code=143)
    assert client.post(f"/runs/{run['id']}/cancel").json()["status"] == "cancelled"
    assert fake_runner.cancelled == [run["id"]]
    assert client.post("/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/cancel").status_code == 404


def test_a_restarted_api_watches_runs_left_running(
    client: TestClient, engine: Engine, data_dir: Path
) -> None:
    run = create(client, run_spec_bytes())
    wait_until(lambda: status_of(client, run["id"]) == "running", "run to be running")
    # A new api process: its runner never launched the run, and says how it ended.
    restarted_runner = FakeRunner()
    restarted_runner.finish(str(run["id"]), RunnerState.FAILED)
    with TestClient(create_app(lambda: build_service(engine, restarted_runner, data_dir))) as again:
        wait_until(lambda: status_of(again, run["id"]) == "failed", "the orphaned run to fail")


def test_run_defaults_name_the_installs_prometheus(engine: Engine, data_dir: Path) -> None:
    """New run fills these in, so its runs collect what tuning sessions' runs do."""
    prometheus = {"url": "http://prometheus.example:9090", "tls": {"mode": "platform"}}
    service = build_service(engine, FakeRunner(), data_dir)
    service.default_prometheus = prometheus
    with TestClient(create_app(lambda: service)) as client:
        assert client.get("/run-defaults").json() == {"prometheus": prometheus, "offline": False}
    service.default_prometheus = None
    with TestClient(create_app(lambda: service)) as client:
        assert client.get("/run-defaults").json() == {"offline": False}
