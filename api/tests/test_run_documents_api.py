"""GET /runs/{id}/analysis, /recommendations, /capacity and /charts/*, typed."""

import json
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

from servtuner_api.datadir import run_bundle_dir
from tests.support.specs import example_bytes, run_spec_bytes

# route suffix -> (bundle path, schema example directory, example name)
DOCUMENTS = {
    "analysis": ("analysis.json", "analysis", "degraded-with-capacity"),
    "recommendations": ("recommendations.json", "recommendations", "sizing-slo-max"),
    "capacity": ("capacity.json", "capacity", "ladder-with-target"),
    "charts/client_timeseries": (
        "charts/client_timeseries.json",
        "chart-client-timeseries",
        "two-stages",
    ),
    "charts/histograms": ("charts/histograms.json", "chart-histograms", "one-stage"),
    "charts/inflight": ("charts/inflight.json", "chart-inflight", "two-stages"),
    "charts/server": ("charts/server.json", "chart-server", "kv-and-cpu"),
    "environment": ("environment.json", "environment", "pod-namespace-knobs"),
    "snapshot": ("target/snapshot-pre.json", "target-snapshot", "router-with-upstream-pre"),
}
UNKNOWN_RUN = "01ARZ3NDEKTSV4RRFFQ69G5FAV"


def new_run(client: TestClient) -> str:
    return str(client.post("/runs", content=run_spec_bytes()).json()["id"])


def write(data_dir: Path, run_id: str, path: str, content: bytes) -> None:
    target = run_bundle_dir(data_dir, run_id) / path
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(content)


@pytest.mark.parametrize("route", sorted(DOCUMENTS))
def test_a_document_is_served_typed(client: TestClient, data_dir: Path, route: str) -> None:
    path, schema_name, example = DOCUMENTS[route]
    run_id = new_run(client)
    assert client.get(f"/runs/{run_id}/{route}").status_code == 404, "not analysed yet"
    content = example_bytes(schema_name, "valid", example)
    write(data_dir, run_id, path, content)
    response = client.get(f"/runs/{run_id}/{route}")
    assert response.status_code == 200, response.text
    assert response.json() == json.loads(content)


@pytest.mark.parametrize("route", sorted(DOCUMENTS))
def test_a_document_breaking_its_schema_is_a_502(
    client: TestClient, data_dir: Path, route: str
) -> None:
    path, _, _ = DOCUMENTS[route]
    run_id = new_run(client)
    write(data_dir, run_id, path, b'{"unexpected": true}')
    response = client.get(f"/runs/{run_id}/{route}")
    assert response.status_code == 502 and path in response.json()["detail"]


def test_unknown_runs_are_404(client: TestClient) -> None:
    for route in DOCUMENTS:
        assert client.get(f"/runs/{UNKNOWN_RUN}/{route}").status_code == 404
        assert client.get(f"/runs/not-a-ulid/{route}").status_code == 404


def test_the_post_snapshot_and_a_bad_phase(client: TestClient, data_dir: Path) -> None:
    run_id = new_run(client)
    content = example_bytes("target-snapshot", "valid", "post-restarted")
    write(data_dir, run_id, "target/snapshot-post.json", content)
    assert client.get(f"/runs/{run_id}/snapshot?phase=post").json() == json.loads(content)
    assert client.get(f"/runs/{run_id}/snapshot?phase=during").status_code == 422
