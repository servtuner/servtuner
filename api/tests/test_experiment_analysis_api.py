"""POST /experiments/{name}/analyze: servtuner compare over the experiment's analysed runs.

A stand-in bench checks the staged bundles and answers with a schema example, so these tests
cover the api's part; bench's comparison has its own tests (bench/internal/analyze/compare).
"""

import json
import stat
from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.bundles import FileBundleStore
from servtuner_api.comparer import Comparer
from servtuner_api.database import create_session_factory
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.indexing import metric_rows
from servtuner_api.records.repository import RunRepository
from servtuner_api.runner import RunnerState
from servtuner_api.service import RunService
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes
from tests.support.waiting import wait_until

# Records its arguments and what each staged bundle holds, then answers with the
# example comparison; COMPARE_FAIL makes it refuse the runs as bench does.
FAKE_BENCH = """#!/bin/sh
[ "$1" = "compare" ] || exit 9
[ -n "$COMPARE_FAIL" ] && { echo "servtuner: run 01X is given more than once" >&2; exit 2; }
here="$(dirname "$0")"
echo "$@" > "$here/argv.txt"
dirs=$(echo "$3" | tr ',' ' ')
: > "$here/staged.txt"
for d in $dirs; do echo "$(basename "$d"): $(ls "$d" | tr '\\n' ' ')" >> "$here/staged.txt"; done
cat "$here/comparison.json"
"""
ANALYSIS = json.loads(example_bytes("analysis", "valid", "degraded-with-capacity"))
COMPARISON = json.loads(example_bytes("comparison", "valid", "winner-with-repeats"))


@pytest.fixture
def bench(tmp_path: Path) -> Path:
    script = tmp_path / "bench"
    script.write_text(FAKE_BENCH)
    script.chmod(script.stat().st_mode | stat.S_IXUSR)
    (tmp_path / "comparison.json").write_text(json.dumps(COMPARISON))
    return script


def service_factory(
    engine: Engine, runner: FakeRunner, data_dir: Path, bench: Path | None
) -> RunService:
    store = FileBundleStore(data_dir)
    return RunService(
        repository=RunRepository(create_session_factory(engine)),
        runner=runner,
        store=store,
        run_token_ttl_seconds=3600,
        runner_poll_seconds=0.02,
        comparer=Comparer(bench, store),
    )


@pytest.fixture
def analyze_client(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path, bench: Path
) -> Iterator[TestClient]:
    app = create_app(lambda: service_factory(engine, fake_runner, data_dir, bench))
    with TestClient(app) as client:
        yield client


def finished_run(
    client: TestClient,
    runner: FakeRunner,
    data_dir: Path,
    *,
    analysed: bool = True,
    state: RunnerState = RunnerState.SUCCEEDED,
) -> str:
    """A run of experiment "concurrency" that finished, with an analysed bundle if asked."""
    response = client.post("/runs", content=run_spec_bytes(experiment="concurrency"))
    assert response.status_code == 201, response.text
    run_id = str(response.json()["id"])
    bundle = run_bundle_dir(data_dir, run_id)
    bundle.mkdir(parents=True, exist_ok=True)
    for name in ("manifest.json", "spec.resolved.json", "requests.parquet"):
        (bundle / name).write_text("{}")
    if analysed:
        (bundle / "analysis.json").write_text(json.dumps(ANALYSIS))
    runner.finish(run_id, state, exit_code=0 if state is RunnerState.SUCCEEDED else 1)
    wait_until(lambda: client.get(f"/runs/{run_id}").json()["status"] == state.value, "final")
    return run_id


def wait_indexed(engine: Engine, run_id: str) -> None:
    repository = RunRepository(create_session_factory(engine))
    wait_until(lambda: bool(repository.list_metrics([run_id])), f"run {run_id} indexed")


def test_an_experiment_is_compared_and_its_latest_analysis_kept(
    analyze_client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path, bench: Path
) -> None:
    analysed = [finished_run(analyze_client, fake_runner, data_dir) for _ in range(2)]
    for run_id in analysed:
        wait_indexed(engine, run_id)
    finished_run(analyze_client, fake_runner, data_dir, analysed=False)
    finished_run(analyze_client, fake_runner, data_dir, state=RunnerState.FAILED)

    response = analyze_client.post("/experiments/concurrency/analyze", json={})
    assert response.status_code == 201, response.text
    analysis = response.json()
    assert analysis["run_ids"] == analysed, "only succeeded, analysed runs are compared"
    assert analysis["objective"] == COMPARISON["objective"]
    assert analysis["comparison"]["recommendation"]["configuration_id"] == "c2"
    argv = (bench.parent / "argv.txt").read_text().split()
    assert argv[:2] == ["compare", "--bundles"] and "--objective" not in argv
    staged = (bench.parent / "staged.txt").read_text()
    assert staged.count("analysis.json") == 2 and "capacity_model.json" not in staged

    detail = analyze_client.get("/experiments/concurrency").json()
    assert detail["latest_analysis"]["id"] == analysis["id"]


def test_an_objective_and_a_subset_of_runs_can_be_chosen(
    analyze_client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path, bench: Path
) -> None:
    first, second = (finished_run(analyze_client, fake_runner, data_dir) for _ in range(2))
    for run_id in (first, second):
        wait_indexed(engine, run_id)
    body = {"objective": "slo_max_rps", "run_ids": [second]}
    response = analyze_client.post("/experiments/concurrency/analyze", json=body)
    assert response.status_code == 201, response.text
    assert response.json()["run_ids"] == [second]
    argv = (bench.parent / "argv.txt").read_text().split()
    assert argv[-2:] == ["--objective", "slo_max_rps"]


def test_bad_analyze_requests(
    analyze_client: TestClient, engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    assert analyze_client.post("/experiments/missing/analyze", json={}).status_code == 404
    unanalysed = finished_run(analyze_client, fake_runner, data_dir, analysed=False)
    assert analyze_client.post("/experiments/concurrency/analyze").status_code == 409
    body = {"run_ids": [unanalysed]}
    response = analyze_client.post("/experiments/concurrency/analyze", json=body)
    assert response.status_code == 422 and unanalysed in response.text
    unknown_objective = {"objective": "fastest"}
    response = analyze_client.post("/experiments/concurrency/analyze", json=unknown_objective)
    assert response.status_code == 422


def test_bench_refusing_the_runs_is_a_422(
    analyze_client: TestClient,
    engine: Engine,
    fake_runner: FakeRunner,
    data_dir: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    wait_indexed(engine, finished_run(analyze_client, fake_runner, data_dir))
    monkeypatch.setenv("COMPARE_FAIL", "1")
    response = analyze_client.post("/experiments/concurrency/analyze", json={})
    assert response.status_code == 422
    assert "given more than once" in response.json()["detail"]
    assert analyze_client.get("/experiments/concurrency").json().get("latest_analysis") is None


def test_without_bench_the_analysis_is_unavailable(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    app = create_app(lambda: service_factory(engine, fake_runner, data_dir, None))
    with TestClient(app) as client:
        run_id = finished_run(client, fake_runner, data_dir)
        RunRepository(create_session_factory(engine)).replace_metrics(run_id, metric_rows(ANALYSIS))
        assert client.post("/experiments/concurrency/analyze").status_code == 503
