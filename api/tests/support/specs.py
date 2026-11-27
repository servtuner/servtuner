"""RunSpec and event bodies built from the shared schema fixtures."""

import json
from pathlib import Path
from typing import Any

EXAMPLES_ROOT = Path(__file__).resolve().parents[3] / "schema" / "examples"


def example_bytes(schema_name: str, kind: str, name: str) -> bytes:
    return (EXAMPLES_ROOT / schema_name / kind / f"{name}.json").read_bytes()


def example_paths(schema_name: str, kind: str) -> list[Path]:
    return sorted((EXAMPLES_ROOT / schema_name / kind).glob("*.json"))


def run_spec_bytes(**changes: Any) -> bytes:
    """The minimal valid RunSpec with top-level fields replaced."""
    document = json.loads(example_bytes("runspec", "valid", "minimal-vllm-mtls"))
    return json.dumps(document | changes).encode()


def event_body(run_id: str, sequence: int, state: str = "running", **changes: Any) -> bytes:
    document = {
        "run_id": run_id,
        "sequence": sequence,
        "time": "2026-10-28T12:05:00Z",
        "step": "load",
        "state": state,
    }
    return json.dumps(document | changes).encode()
