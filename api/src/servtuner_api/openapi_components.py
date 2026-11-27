"""Request-body models in the OpenAPI document, for the frontend's generated client."""

import json
from typing import Any

from fastapi import FastAPI
from pydantic import BaseModel


def publish_request_models(app: FastAPI, models: list[type[BaseModel]]) -> None:
    """Add the generated request-body models to the OpenAPI components.

    FastAPI only emits models that a signature mentions; the raw-body routes
    mention none, yet the generated TypeScript client needs them.
    """
    original_openapi = app.openapi

    def openapi() -> dict[str, Any]:
        if app.openapi_schema is None:
            document = original_openapi()
            schemas = document.setdefault("components", {}).setdefault("schemas", {})
            for model in models:
                model_schema = model.model_json_schema(ref_template="#/components/schemas/{model}")
                _merge_components(schemas, model.__name__, model_schema)
        return app.openapi_schema or {}

    app.openapi = openapi  # type: ignore[method-assign]


def _merge_components(schemas: dict[str, Any], name: str, model_schema: dict[str, Any]) -> None:
    """Add a request model and its sub-schemas without overwriting another one.

    Generated models in different schema files share short names (the RunSpec's
    Workload and the snapshot's Workload, two enums called Kind). A sub-schema
    whose name is taken by a different schema is renamed ``<Model>_<Name>``, and
    the model's references follow, so no response type is silently replaced.
    """
    definitions: dict[str, Any] = model_schema.pop("$defs", {})
    renames = {
        sub: f"{name}_{sub}"
        for sub, schema in definitions.items()
        if sub in schemas and schemas[sub] != schema
    }
    # A sub-schema that refers to a renamed one changes when its reference is
    # rewritten, so it is renamed too, until nothing more changes.
    while True:
        referring = {
            sub: f"{name}_{sub}"
            for sub, schema in definitions.items()
            if sub in schemas
            and sub not in renames
            and any(f'"#/components/schemas/{old}"' in json.dumps(schema) for old in renames)
        }
        if not referring:
            break
        renames.update(referring)
    if renames:
        text = json.dumps({"model": model_schema, "defs": definitions})
        for old, new in renames.items():
            text = text.replace(f'"#/components/schemas/{old}"', f'"#/components/schemas/{new}"')
        rewritten = json.loads(text)
        model_schema, definitions = rewritten["model"], rewritten["defs"]
        definitions = {renames.get(sub, sub): schema for sub, schema in definitions.items()}
    schemas.update(definitions)
    schemas[name] = model_schema
