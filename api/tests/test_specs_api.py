"""POST /specs/validate: servtuner validate's report on a RunSpec, YAML or JSON.

These run the real bench binary (built once per session) when Go is available,
so the api and bench agree on the report's shape; otherwise they are skipped.
"""

import json
from collections.abc import Iterator
from pathlib import Path

import pytest
from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.bundles import FileBundleStore
from servtuner_api.database import create_session_factory
from servtuner_api.records.repository import RunRepository
from servtuner_api.service import RunService
from servtuner_api.specs import SpecValidator
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes


def service(engine: Engine, runner: FakeRunner, data_dir: Path, bench: Path | None) -> RunService:
    return RunService(
        repository=RunRepository(create_session_factory(engine)),
        runner=runner,
        store=FileBundleStore(data_dir),
        run_token_ttl_seconds=3600,
        runner_poll_seconds=0.02,
        validator=SpecValidator(bench),
    )


@pytest.fixture
def validate_client(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path, bench_binary: Path
) -> Iterator[TestClient]:
    with TestClient(
        create_app(lambda: service(engine, fake_runner, data_dir, bench_binary))
    ) as client:
        yield client


def test_a_valid_spec_in_json_or_yaml(validate_client: TestClient) -> None:
    document = json.loads(example_bytes("runspec", "valid", "off-cluster-vllm-platform"))
    response = validate_client.post("/specs/validate", content=json.dumps(document))
    assert response.status_code == 200, response.text
    expected: dict[str, object] = {
        "valid": True,
        "name": "vllm-lan-baseline",
        "errors": [],
        "warnings": [],
    }
    assert response.json() == expected
    as_yaml = validate_client.post("/specs/validate", content=to_yaml(document))
    assert as_yaml.json() == expected, to_yaml(document)


def test_problems_and_warnings_are_listed(validate_client: TestClient) -> None:
    missing = validate_client.post(
        "/specs/validate", content='{"apiVersion":"servtuner.dev/v1","kind":"RunSpec","name":"x"}'
    )
    report = missing.json()
    assert missing.status_code == 200 and report["valid"] is False
    assert any("missing properties" in problem for problem in report["errors"])
    document = json.loads(example_bytes("runspec", "valid", "off-cluster-vllm-platform"))
    document["load"]["stages"] = [{"rate": 2, "duration": "20s"}]
    short = validate_client.post("/specs/validate", content=json.dumps(document)).json()
    assert short["valid"] is True and any(
        "shorter than the minimum" in warning for warning in short["warnings"]
    )
    assert validate_client.post("/specs/validate", content="not: [valid").json()["valid"] is False


def test_without_bench_validation_is_unavailable(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    with TestClient(create_app(lambda: service(engine, fake_runner, data_dir, None))) as client:
        assert client.post("/specs/validate", content="{}").status_code == 503


def to_yaml(value: object, indent: str = "") -> str:
    """A block-style YAML rendering of a JSON document, for the YAML case."""
    if isinstance(value, dict):
        lines = []
        for key, item in value.items():
            if isinstance(item, (dict, list)) and item:
                lines.append(f"{indent}{key}:\n{to_yaml(item, indent + '  ')}")
            else:
                lines.append(f"{indent}{key}: {json.dumps(item)}")
        return "\n".join(lines)
    if isinstance(value, list):
        return "\n".join(f"{indent}- {to_yaml(item, indent + '  ').lstrip()}" for item in value)
    return f"{indent}{json.dumps(value)}"


STARTER = Path(__file__).resolve().parents[2] / "frontend/src/features/new-run/starter.ts"


def test_the_new_run_template_is_valid(validate_client: TestClient) -> None:
    """The New run view opens on this template, so bench must accept it as it is."""
    source = STARTER.read_text()
    start = source.index("`") + 1  # STARTER_SPEC is the file's first template literal
    template = source[start : source.index("`", start)]
    report = validate_client.post("/specs/validate", content=template).json()
    assert report["valid"] is True and report["errors"] == [], report
    assert report["warnings"] == [], "a template that warns teaches people to ignore warnings"


def test_workload_presets_come_from_bench(validate_client: TestClient) -> None:
    response = validate_client.get("/workload-presets")
    assert response.status_code == 200
    presets = response.json()
    names = [f"{preset['name']}@{preset['version']}" for preset in presets]
    assert "chat-short@1" in names
    assert "rag-prefill@1" in names
    rag = next(preset for preset in presets if preset["name"] == "rag-prefill")
    assert rag["workload"]["dataset"]["shared_prefix"] == {"tokens": 512, "groups": 4}
    assert rag["slos"]["ttft_p95"] == "2s"


def test_search_spaces_come_from_bench(validate_client: TestClient) -> None:
    """New session's starting spaces: the catalog bench carries, served as it is."""
    response = validate_client.get("/search-spaces")
    assert response.status_code == 200
    spaces = {space["name"]: space for space in response.json()}
    assert set(spaces) == {"single-gpu", "memory-bound", "two-gpu-tensor-parallel", "no-gpu"}
    assert spaces["no-gpu"]["fits"] == {"placements": ["kubernetes"], "min_gpus": 0, "max_gpus": 0}
    assert spaces["no-gpu"]["space"]["objective"] == "slo_max_rps"
    assert "host" in spaces["memory-bound"]["fits"]["placements"]


def test_workload_presets_need_bench(
    engine: Engine, fake_runner: FakeRunner, data_dir: Path
) -> None:
    with TestClient(create_app(lambda: service(engine, fake_runner, data_dir, None))) as client:
        assert client.get("/workload-presets").status_code == 503


def test_the_knob_catalog_comes_from_bench(validate_client: TestClient) -> None:
    """Every vLLM flag with vLLM's own description and limits, as bench carries it."""
    response = validate_client.get("/knobs")
    assert response.status_code == 200, response.text
    catalog = response.json()
    knobs = {knob["name"]: knob for knob in catalog["knobs"]}
    assert catalog["vllm_version"] == "v0.34.0"
    seqs = knobs["vllm.max_num_seqs"]
    assert seqs["label"] == "Max sequences" and seqs["flag"] == "--max-num-seqs"
    assert seqs["built_in"] is True and seqs["limits"] == {"ge": 1}
    assert seqs["help"].startswith("Maximum number of sequences")
    assert knobs["vllm.max_num_scheduled_tokens"]["built_in"] is False
