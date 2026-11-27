"""Reading servtuner 0.1 data: the api's upgrade agrees with bench's on the same fixtures."""

import json
from pathlib import Path
from typing import Any

import pytest

from servtuner_api.legacy import (
    upgrade_bundle_document,
    upgrade_legacy_environment,
    upgrade_legacy_run_spec,
    upgrade_legacy_run_spec_raw,
    upgrade_legacy_tls,
)
from servtuner_api.models import parse_run_spec, validate

REPOSITORY = Path(__file__).resolve().parents[2]
# bench's fixtures: the examples as servtuner 0.1 wrote them (spec/legacy_test.go reads the same).
LEGACY = REPOSITORY / "bench" / "internal" / "spec" / "testdata" / "legacy"
EXAMPLES = REPOSITORY / "schema" / "examples" / "runspec" / "valid"


def load(path: Path) -> Any:
    return json.loads(path.read_text())


LEGACY_RUN_SPECS = sorted(LEGACY.glob("runspec-v1-*.json"))


def test_there_are_legacy_fixtures() -> None:
    assert LEGACY_RUN_SPECS


@pytest.mark.parametrize("legacy", LEGACY_RUN_SPECS, ids=lambda path: path.name)
def test_a_legacy_run_spec_upgrades_to_the_converted_example(legacy: Path) -> None:
    upgraded = upgrade_legacy_run_spec(load(legacy))
    current = EXAMPLES / legacy.name.removeprefix("runspec-v1-")
    assert upgraded == load(current)
    parse_run_spec(upgraded)  # and it is a valid RunSpec


def test_a_v2_run_spec_is_left_alone() -> None:
    raw = (EXAMPLES / "minimal-vllm-client-cert.json").read_bytes()
    assert upgrade_legacy_run_spec_raw(raw) is raw
    assert upgrade_legacy_run_spec(json.loads(raw)) == json.loads(raw)


def test_stored_v1_bytes_become_v2() -> None:
    legacy = LEGACY_RUN_SPECS[0]
    upgraded = json.loads(upgrade_legacy_run_spec_raw(legacy.read_bytes()))
    assert upgraded["apiVersion"] == "servtuner.dev/v2"
    parse_run_spec(upgraded)


def test_a_legacy_environment_upgrades() -> None:
    legacy = load(LEGACY / "environment-v1.json")
    upgraded = upgrade_bundle_document("environment", legacy)
    validate("environment", upgraded)
    assert upgraded["transport"] == {
        "target": {"tls": True, "client_cert": True},
        "prometheus": {"tls": False, "client_cert": False},
        "api": {"tls": True, "client_cert": True},
        "object_store": {"tls": False, "client_cert": False},
    }
    assert legacy["transport"]["target"] == "mtls"  # the original is not changed
    assert upgrade_legacy_environment(upgraded) is upgraded


@pytest.mark.parametrize(
    ("legacy", "upgraded"),
    [
        (
            {"mode": "mtls", "secretRef": {"name": "c"}},
            {"enabled": True, "clientCert": {"secretRef": {"name": "c"}}},
        ),
        (
            {"mode": "tls", "caSecretRef": {"name": "ca"}},
            {"enabled": True, "caSecretRef": {"name": "ca"}},
        ),
        ({"mode": "platform"}, {"enabled": False}),
        # Other keys are kept, so a block that was invalid stays invalid.
        (
            {"mode": "platform", "secretRef": {"name": "c"}},
            {"enabled": False, "secretRef": {"name": "c"}},
        ),
        ({"mode": "insecure"}, None),
    ],
)
def test_tls_blocks(legacy: dict[str, object], upgraded: dict[str, object] | None) -> None:
    assert upgrade_legacy_tls(legacy) == upgraded


def test_other_bundle_documents_pass_through() -> None:
    document = {"apiVersion": "servtuner.dev/v1", "transport": {"target": "mtls"}}
    assert upgrade_bundle_document("manifest", document) is document
