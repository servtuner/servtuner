"""POST /runs/{id}/followup and /runs/{id}/confirm-applied."""

import json
from pathlib import Path
from typing import Any

from fastapi.testclient import TestClient

from servtuner_api.datadir import run_bundle_dir
from servtuner_api.models import validate
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes

PATCH = "providers:\n  servtuner-mock:\n    concurrency_and_buffer_size:\n      concurrency: 128\n"


def recommendations_with_knob() -> dict[str, Any]:
    document = json.loads(example_bytes("recommendations", "valid", "sizing-slo-max"))
    resource = document["recommendations"][0]
    knob: dict[str, Any] = {
        **resource,
        "id": "B1/target",
        "rule": "B1",
        "subject": {"kind": "knob", "name": "bifrost.concurrency"},
        "unit": "requests",
        "current": 32,
        "recommended": 128,
        "caveats": [],
    }
    knob.pop("derivation_node", None)
    document["recommendations"].append(knob)
    validate("recommendations", document)
    return document


def analysed_run(
    client: TestClient, data_dir: Path, patch: bool = True, experiment: str | None = "concurrency"
) -> str:
    fields: dict[str, Any] = {"knobs": {"bifrost.concurrency": 32}}
    if experiment is not None:
        fields["experiment"] = experiment
    body = run_spec_bytes(**fields)
    run_id = str(client.post("/runs", content=body).json()["id"])
    bundle = run_bundle_dir(data_dir, run_id)
    (bundle / "patches").mkdir(parents=True, exist_ok=True)
    (bundle / "recommendations.json").write_text(json.dumps(recommendations_with_knob()))
    if patch:
        (bundle / "patches" / "bifrost.yaml").write_text(PATCH)
    return run_id


def test_a_followup_creates_waiting_runs_with_only_the_knob_changed(
    client: TestClient, data_dir: Path, fake_runner: FakeRunner
) -> None:
    run_id = analysed_run(client, data_dir)
    launched_before = set(fake_runner.requests)
    response = client.post(f"/runs/{run_id}/followup", json={"recommendation_id": "B1/target"})
    assert response.status_code == 201, response.text
    followup = response.json()
    assert followup["knob"] == "bifrost.concurrency" and followup["value"] == 128
    assert followup["patch"] == PATCH
    runs = followup["runs"]
    assert len(runs) == 3
    for run in runs:
        assert run["status"] == "awaiting_config"
        assert run["experiment"] == "concurrency"
        assert run["knobs"]["bifrost.concurrency"] == 128
        assert run["name"].endswith("-followup")
    assert (
        set(fake_runner.requests) == launched_before
    ), "nothing launches before the change is applied"


def test_followups_of_a_run_without_an_experiment_get_one(
    client: TestClient, data_dir: Path
) -> None:
    run_id = analysed_run(client, data_dir, experiment=None)
    source = client.get(f"/runs/{run_id}").json()
    assert "experiment" not in source or source["experiment"] is None
    body = {"recommendation_id": "B1/target", "repeats": 2}
    runs = client.post(f"/runs/{run_id}/followup", json=body).json()["runs"]
    expected = f"{source['name']}-bifrost-concurrency"
    assert [run["experiment"] for run in runs] == [expected, expected]


def test_a_named_experiment_wins_over_the_source_runs(client: TestClient, data_dir: Path) -> None:
    run_id = analysed_run(client, data_dir)
    body = {"recommendation_id": "B1/target", "repeats": 1, "experiment": "concurrency-128"}
    runs = client.post(f"/runs/{run_id}/followup", json=body).json()["runs"]
    assert runs[0]["experiment"] == "concurrency-128"


def test_confirming_launches_a_waiting_run_once(
    client: TestClient, data_dir: Path, fake_runner: FakeRunner
) -> None:
    run_id = analysed_run(client, data_dir)
    body = {"recommendation_id": "B1/target", "repeats": 1}
    waiting = client.post(f"/runs/{run_id}/followup", json=body).json()["runs"][0]["id"]
    confirmed = client.post(f"/runs/{waiting}/confirm-applied")
    assert confirmed.status_code == 200, confirmed.text
    assert confirmed.json()["status"] in ("launching", "pending", "running")
    assert waiting in fake_runner.requests
    spec = json.loads(fake_runner.requests[waiting].spec_raw)
    assert spec["knobs"]["bifrost.concurrency"] == 128
    assert client.post(f"/runs/{waiting}/confirm-applied").status_code == 409
    assert client.post(f"/runs/{run_id}/confirm-applied").status_code == 409


def test_what_cannot_be_followed_up(client: TestClient, data_dir: Path) -> None:
    run_id = analysed_run(client, data_dir, patch=False)
    resource = client.post(
        f"/runs/{run_id}/followup", json={"recommendation_id": "sizing.cpu.request/target/bifrost"}
    )
    assert resource.status_code == 422 and "not a knob change" in resource.json()["detail"]
    unknown = client.post(f"/runs/{run_id}/followup", json={"recommendation_id": "nope"})
    assert unknown.status_code == 422
    no_patch = client.post(
        f"/runs/{run_id}/followup", json={"recommendation_id": "B1/target", "repeats": 1}
    )
    assert no_patch.status_code == 201 and "patch" not in no_patch.json()
    fresh = str(client.post("/runs", content=run_spec_bytes()).json()["id"])
    assert (
        client.post(f"/runs/{fresh}/followup", json={"recommendation_id": "B1/target"}).status_code
        == 422
    )
    assert (
        client.post(
            "/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/followup", json={"recommendation_id": "x"}
        ).status_code
        == 404
    )
    assert (
        client.post(
            f"/runs/{run_id}/followup", json={"recommendation_id": "B1/target", "repeats": 11}
        ).status_code
        == 422
    )
