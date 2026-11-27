"""The checked-in openapi.json is the contract with the frontend."""

import json
from pathlib import Path

from servtuner_api.openapi import render_openapi

OPENAPI_FILE = Path(__file__).resolve().parents[1] / "openapi.json"


def test_checked_in_document_is_current() -> None:
    assert OPENAPI_FILE.read_text() == render_openapi()


def test_document_embeds_the_generated_models_and_routes() -> None:
    document = json.loads(render_openapi())
    assert set(document["paths"]) == {
        "/healthz",
        "/version",
        "/auth/status",
        "/auth/login",
        "/auth/logout",
        "/auth/setup",
        "/auth/me",
        "/auth/password",
        "/auth/sessions",
        "/auth/sessions/{session_id}",
        "/auth/sessions/revoke-others",
        "/users",
        "/users/{user_id}",
        "/users/{user_id}/password",
        "/users/{user_id}/identities",
        "/users/{user_id}/sessions",
        "/audit",
        "/runs",
        "/runs/{run_id}",
        "/runs/{run_id}/events",
        "/runs/{run_id}/cancel",
        "/runs/{run_id}/files",
        "/runs/{run_id}/files/{path}",
        "/plans",
        "/targets",
        "/targets/check",
        "/targets/{target_id}",
        "/targets/{target_id}/check",
        "/targets/{target_id}/credentials/{slot}",
        "/compare",
        "/experiments",
        "/experiments/{name}",
        "/experiments/{name}/analyze",
        "/runs/{run_id}/analysis",
        "/runs/{run_id}/recommendations",
        "/runs/{run_id}/capacity",
        "/runs/{run_id}/charts/client_timeseries",
        "/runs/{run_id}/charts/histograms",
        "/runs/{run_id}/charts/inflight",
        "/runs/{run_id}/charts/server",
        "/runs/{run_id}/followup",
        "/runs/{run_id}/environment",
        "/runs/{run_id}/snapshot",
        "/runs/{run_id}/confirm-applied",
        "/specs/validate",
        "/workload-presets",
        "/managed-targets",
        "/managed-targets/{target_id}",
        "/managed-targets/{target_id}/deploy",
        "/managed-targets/{target_id}/readiness",
        "/managed-targets/{target_id}/logs",
        "/search-spaces",
        "/knobs",
        "/run-defaults",
        "/runs/{run_id}/delete-impact",
        "/experiments/{name}/delete-impact",
        "/managed-targets/{target_id}/check-launcher",
        "/hosts",
        "/hosts/{host_id}",
        "/hosts/{host_id}/read-machine",
        "/price-book",
        "/managed-targets/{target_id}/credentials/{slot}",
        "/managed-targets/{target_id}/scale-down",
        "/managed-targets/{target_id}/boot-probe",
        "/tuning-sessions",
        "/tuning-sessions/{session_id}",
        "/tuning-sessions/{session_id}/approve",
        "/tuning-sessions/{session_id}/cancel",
        "/schedules",
        "/schedules/{schedule_id}",
        "/schedules/{schedule_id}/trigger",
        "/scenarios",
        "/scenarios/evaluate",
        "/scenarios/{scenario_id}",
        "/price-book/datasheet",
        "/runs/{run_id}/datasheet",
    }
    schemas = set(document["components"]["schemas"])
    assert {"RunSpec", "Event", "Summary", "RunResource", "PlanCreate", "Plan"} <= schemas
    create_body = document["paths"]["/runs"]["post"]["requestBody"]["content"]["application/json"]
    assert create_body["schema"] == {"$ref": "#/components/schemas/RunSpec"}


def test_render_is_deterministic() -> None:
    assert render_openapi() == render_openapi()


def test_plans_take_a_plan_create_body() -> None:
    document = json.loads(render_openapi())
    body = document["paths"]["/plans"]["post"]["requestBody"]["content"]["application/json"]
    assert body["schema"] == {"$ref": "#/components/schemas/PlanCreate"}


def test_publishing_request_models_never_replaces_a_response_schema() -> None:
    """Generated models share short names across schema files (two Workloads, two
    Kinds); a request model's sub-schema must not overwrite a response's."""
    from typing import NoReturn

    from fastapi.openapi.utils import get_openapi

    from servtuner_api.app import create_app

    def no_service() -> NoReturn:
        raise RuntimeError("never started")

    app = create_app(no_service)
    plain = get_openapi(title=app.title, version=app.version, routes=app.routes)
    published = app.openapi()
    # The published models themselves are re-published as request bodies on
    # purpose (Event is also a response, SessionCreate is a session's `request`);
    # their sub-schemas must never replace one.
    published_models = {"RunSpec", "Event", "PlanCreate", "SessionCreate"}
    for name, schema in plain["components"]["schemas"].items():
        if name not in published_models:
            assert published["components"]["schemas"][name] == schema, f"{name} was replaced"
    schemas = published["components"]["schemas"]
    assert "pods" in schemas["Workload"]["properties"]
    assert "knob" in schemas["Kind"]["enum"]
    assert schemas["RunSpec"]["properties"]["workload"] == {
        "$ref": "#/components/schemas/RunSpec_Workload"
    }


def test_every_ref_resolves() -> None:
    """A raw-body route whose model was never published leaves a dangling $ref,
    which only openapi-typescript notices (it fails `just gen`)."""
    document = json.loads(render_openapi())
    components = document["components"]["schemas"]

    def refs(node: object) -> list[str]:
        if isinstance(node, dict):
            found = [node["$ref"]] if isinstance(node.get("$ref"), str) else []
            return found + [ref for value in node.values() for ref in refs(value)]
        if isinstance(node, list):
            return [ref for item in node for ref in refs(item)]
        return []

    prefix = "#/components/schemas/"
    dangling = {ref for ref in refs(document) if ref.removeprefix(prefix) not in components}
    assert not dangling
