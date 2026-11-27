"""Cross-language conformance: the Python half of the schema contract.

The Go half is bench/internal/spec/conformance_test.go. Both iterate over the
same fixtures in schema/examples ("Tests share fixtures"):
1. every example validates (or not) against the JSON Schema as its directory says;
2. every valid example survives a round trip through the generated models.
"""

import json
from pathlib import Path
from typing import Any

import pytest
import yaml
from jsonschema import Draft7Validator
from pydantic import BaseModel, ValidationError

from servtuner_api.models import (
    AnalysisResult,
    BootProbeRequest,
    BootResults,
    CapacityTable,
    CheckTargetRequest,
    CheckTargetResult,
    ClientSample,
    ClientTimeseries,
    Comparison,
    Environment,
    Event,
    HostMachine,
    InFlightChart,
    InfraPricing,
    KnobCatalog,
    LatencyHistograms,
    Manifest,
    Phases,
    Plan,
    PlanRequest,
    PrometheusQueries,
    PrometheusTargets,
    Proposal,
    Recommendations,
    RequestRecord,
    RunCapacityModel,
    RunSpec,
    SchemaValidationError,
    SearchSpace,
    SearchSpaceTemplate,
    ServerChart,
    Summary,
    TargetSnapshot,
    TokenPricing,
    WorkloadPreset,
    parse_run_spec,
    validate,
)
from servtuner_api.models.generated.runspec_schema import TlsMtls, TlsPlatform
from servtuner_api.models.validation import schema_dir, schema_names

SCHEMA_ROOT = Path(__file__).resolve().parents[2] / "schema"
EXAMPLES_ROOT = SCHEMA_ROOT / "examples"

# Schema name (the examples directory name) -> generated model.
MODELS: dict[str, type[BaseModel]] = {
    "runspec": RunSpec,
    "manifest": Manifest,
    "phases": Phases,
    "environment": Environment,
    "summary": Summary,
    "request-record": RequestRecord,
    "events": Event,
    "prometheus-queries": PrometheusQueries,
    "prometheus-targets": PrometheusTargets,
    "target-snapshot": TargetSnapshot,
    "client-sample": ClientSample,
    "analysis": AnalysisResult,
    "recommendations": Recommendations,
    "capacity": CapacityTable,
    "capacity-model": RunCapacityModel,
    "plan-request": PlanRequest,
    "plan": Plan,
    "comparison": Comparison,
    "chart-client-timeseries": ClientTimeseries,
    "chart-histograms": LatencyHistograms,
    "chart-inflight": InFlightChart,
    "chart-server": ServerChart,
    "infra-pricing": InfraPricing,
    "token-pricing": TokenPricing,
    "check-target-request": CheckTargetRequest,
    "check-target-result": CheckTargetResult,
    "workload-preset": WorkloadPreset,
    "search-space": SearchSpace,
    "search-space-template": SearchSpaceTemplate,
    "boot-probe-request": BootProbeRequest,
    "boot-results": BootResults,
    "proposal": Proposal,
    "knob-catalog": KnobCatalog,
    "host-machine": HostMachine,
}


def _load_json(path: Path) -> Any:
    return json.loads(path.read_text())


def validation_errors(schema_name: str, document: Any) -> list[str]:
    try:
        validate(schema_name, document)
    except SchemaValidationError as error:
        return error.violations
    return []


def example_files(schema_name: str, validity: str) -> list[Path]:
    return sorted((EXAMPLES_ROOT / schema_name / validity).glob("*.json"))


ALL_EXAMPLES = [
    pytest.param(name, validity, path, id=f"{name}/{validity}/{path.name}")
    for name in sorted(MODELS)
    for validity in ("valid", "invalid")
    for path in example_files(name, validity)
]
VALID_EXAMPLES = [case for case in ALL_EXAMPLES if case.values[1] == "valid"]
INVALID_RUNSPEC_EXAMPLES = [
    case for case in ALL_EXAMPLES if case.values[0] == "runspec" and case.values[1] == "invalid"
]


def test_schemas_are_draft_07_and_match_models() -> None:
    assert set(schema_names()) == set(MODELS)
    for schema_file in schema_dir().rglob("*.schema.json"):
        schema = _load_json(schema_file)
        assert schema["$schema"] == "http://json-schema.org/draft-07/schema#", schema_file
        Draft7Validator.check_schema(schema)


def test_every_schema_has_examples() -> None:
    for name in MODELS:
        assert example_files(name, "valid"), f"{name} has no valid examples"
        assert example_files(name, "invalid"), f"{name} has no invalid examples"
    assert len(example_files("runspec", "valid")) >= 3
    assert len(example_files("runspec", "invalid")) >= 5


@pytest.mark.parametrize(("name", "validity", "path"), ALL_EXAMPLES)
def test_example_matches_schema(name: str, validity: str, path: Path) -> None:
    errors = validation_errors(name, _load_json(path))
    if validity == "valid":
        assert not errors, errors
    else:
        assert errors, "invalid example was accepted"


def _find_missing(prefix: str, want: Any, got: Any) -> str:
    """Path of the first value in ``want`` that ``got`` lacks or changes, else ''."""
    if isinstance(want, dict):
        if not isinstance(got, dict):
            return prefix
        for key in sorted(want):
            missing = _find_missing(f"{prefix}/{key}", want[key], got.get(key))
            if missing:
                return missing
        return ""
    if isinstance(want, list):
        if not isinstance(got, list) or len(got) != len(want):
            return prefix
        for index, item in enumerate(want):
            missing = _find_missing(f"{prefix}/{index}", item, got[index])
            if missing:
                return missing
        return ""
    return "" if want == got else prefix


@pytest.mark.parametrize(("name", "validity", "path"), VALID_EXAMPLES)
def test_valid_example_round_trips(name: str, validity: str, path: Path) -> None:
    model = MODELS[name]
    original = _load_json(path)
    first = model.model_validate(original)
    # exclude_none: optional fields are absent in the schema, never null.
    remarshaled = first.model_dump(mode="json", exclude_none=True)
    assert not validation_errors(name, remarshaled), remarshaled
    assert model.model_validate(remarshaled) == first
    assert _find_missing("", original, remarshaled) == ""


# Rules written with if/then or oneOf: the generators ignore them, so only the
# schema validator (parse_run_spec) can reject these, never the bare model.
VALIDATOR_ONLY_INVALID_RUNSPECS = {
    "pacing-sine-without-sine-block.json",
    "admin-no-credential-keys.json",
    "admin-password-key-missing.json",
    "admin-token-and-password.json",
    "infra-pricing-static-without-table.json",
    "infra-pricing-gpu-both-forms.json",
    "token-pricing-manual-without-prices.json",
    "token-pricing-price-book-with-url.json",
    "workload-preset-with-dataset.json",
    "workload-preset-with-conversation.json",
    "workload-without-dataset-or-preset.json",
}


MODEL_REJECTED_INVALID_RUNSPECS = [
    pytest.param("runspec", "invalid", path, id=f"runspec/invalid/{path.name}")
    for path in example_files("runspec", "invalid")
    if path.name not in VALIDATOR_ONLY_INVALID_RUNSPECS
]


@pytest.mark.parametrize(("name", "validity", "path"), MODEL_REJECTED_INVALID_RUNSPECS)
def test_generated_runspec_model_rejects_invalid_examples(
    name: str, validity: str, path: Path
) -> None:
    with pytest.raises(ValidationError):
        RunSpec.model_validate(_load_json(path))


def test_defaults_are_applied() -> None:
    """The same shared expectations as Go's TestDefaultsAreApplied."""
    defaults = EXAMPLES_ROOT / "runspec" / "defaults"
    expected = _load_json(defaults / "expected.json")
    assert not validation_errors("runspec", expected)
    applied = parse_run_spec(_load_json(defaults / "input.json"))
    assert applied.model_dump(mode="json", exclude_none=True) == expected


def test_tls_variants_are_typed() -> None:
    mtls = parse_run_spec(
        _load_json(EXAMPLES_ROOT / "runspec" / "valid" / "minimal-vllm-mtls.json")
    )
    platform = parse_run_spec(
        _load_json(EXAMPLES_ROOT / "runspec" / "valid" / "router-bifrost-platform.json")
    )
    assert isinstance(mtls.target.tls.root, TlsMtls)
    assert isinstance(platform.target.tls.root, TlsPlatform)


@pytest.mark.parametrize(("name", "validity", "path"), INVALID_RUNSPEC_EXAMPLES)
def test_parse_run_spec_rejects_invalid_examples(name: str, validity: str, path: Path) -> None:
    with pytest.raises(SchemaValidationError):
        parse_run_spec(_load_json(path))


def test_parse_run_spec_rejects_what_pydantic_alone_accepts() -> None:
    document = _load_json(EXAMPLES_ROOT / "runspec" / "valid" / "minimal-vllm-mtls.json")
    document["experiment"] = None
    RunSpec.model_validate(document)  # the generated model tolerates an explicit null
    with pytest.raises(SchemaValidationError) as caught:
        parse_run_spec(document)
    assert any("$.experiment" in violation for violation in caught.value.violations)


def test_validation_error_lists_every_violation() -> None:
    document = _load_json(EXAMPLES_ROOT / "runspec" / "valid" / "minimal-vllm-mtls.json")
    document["name"] = "Bad_Name"
    document["extra"] = 1
    with pytest.raises(SchemaValidationError) as caught:
        validate("runspec", document)
    assert len(caught.value.violations) == 2


PRESET_FILES = sorted((SCHEMA_ROOT / "presets").glob("*.yaml"))


def test_preset_files_exist() -> None:
    names = {path.name for path in PRESET_FILES}
    assert {
        "chat-short.v1.yaml",
        "chat-multiturn.v1.yaml",
        "decode-long.v1.yaml",
        "summarize.v1.yaml",
    } <= names


@pytest.mark.parametrize("path", PRESET_FILES, ids=lambda path: path.name)
def test_preset_file_matches_schema(path: Path) -> None:
    """The Go half is TestEmbeddedPresetsAreTheCatalog (WorkloadPresets validates each file)."""
    document = yaml.safe_load(path.read_text())
    assert not validation_errors("workload-preset", document)
    assert path.name == f"{document['name']}.v{document['version']}.yaml"
    WorkloadPreset.model_validate(document)
