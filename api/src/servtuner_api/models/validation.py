"""Schema validation for documents that cross the API boundary.

The generated pydantic models accept things the JSON Schema forbids, for
example an explicit ``null`` for an optional field. Anything from a user, a
file or the network must therefore go through ``validate`` or
``parse_run_spec`` rather than ``RunSpec.model_validate`` alone. This mirrors
bench's ``spec.Validate`` and ``spec.ParseRunSpec``.

Where the schemas come from: ``$SERVTUNER_SCHEMA_DIR`` if set (the container
image copies ``schema/json`` there), otherwise ``schema/json`` in the
repository checkout this package is installed from (editable installs, tests
and CI). A missing directory is an error, never a silent skip.
"""

import json
import os
from functools import cache
from pathlib import Path
from typing import Any

from jsonschema import Draft7Validator
from referencing import Registry, Resource
from referencing.jsonschema import DRAFT7

from servtuner_api.models.generated.runspec_schema import RunSpec

SCHEMA_DIR_ENV = "SERVTUNER_SCHEMA_DIR"
SCHEMA_SUFFIX = ".schema.json"
SHARED_DEFINITIONS_NAME = "common"
# src/servtuner_api/models/validation.py -> repository root
_CHECKOUT_SCHEMA_DIR = Path(__file__).resolve().parents[4] / "schema" / "json"


class SchemaValidationError(ValueError):
    """A document broke a schema. ``violations`` lists every one, with its JSON path."""

    def __init__(self, schema_name: str, violations: list[str]) -> None:
        self.schema_name = schema_name
        self.violations = violations
        super().__init__(f"invalid {schema_name} document: " + "; ".join(violations))


def schema_dir() -> Path:
    configured = os.environ.get(SCHEMA_DIR_ENV)
    directory = Path(configured) if configured else _CHECKOUT_SCHEMA_DIR
    if not directory.is_dir():
        raise FileNotFoundError(
            f"schema directory {directory} not found; set {SCHEMA_DIR_ENV} to a copy of schema/json"
        )
    return directory


@cache
def _load_schemas() -> tuple[Registry, dict[str, Any]]:
    """Register every schema by ``$id`` so cross-file ``$ref``s resolve offline."""
    registry: Registry = Registry()
    schemas_by_name: dict[str, Any] = {}
    for schema_file in sorted(schema_dir().rglob("*" + SCHEMA_SUFFIX)):
        schema = json.loads(schema_file.read_text())
        registry = registry.with_resource(
            schema["$id"], Resource.from_contents(schema, default_specification=DRAFT7)
        )
        name = schema_file.name.removesuffix(SCHEMA_SUFFIX)
        if name != SHARED_DEFINITIONS_NAME:
            schemas_by_name[name] = schema
    return registry, schemas_by_name


def schema_names() -> list[str]:
    return sorted(_load_schemas()[1])


def validate(schema_name: str, document: object) -> None:
    """Raise ``SchemaValidationError`` listing every violation, or return."""
    registry, schemas_by_name = _load_schemas()
    if schema_name not in schemas_by_name:
        raise KeyError(f"unknown schema {schema_name!r}; known: {sorted(schemas_by_name)}")
    validator = Draft7Validator(schemas_by_name[schema_name], registry=registry)
    violations = [
        f"{error.json_path}: {error.message}"
        for error in sorted(validator.iter_errors(document), key=lambda e: e.json_path)
    ]
    if violations:
        raise SchemaValidationError(schema_name, violations)


def parse_run_spec(document: object) -> RunSpec:
    """Validate a RunSpec against the schema, then build the model."""
    validate("runspec", document)
    return RunSpec.model_validate(document)
