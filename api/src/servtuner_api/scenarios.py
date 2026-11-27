"""Plans and saved scenarios."""

from typing import Any

from servtuner_api.bundles import BundleStore, bundle_key
from servtuner_api.database import RunRow, ScenarioRow
from servtuner_api.documents import parse_json_document
from servtuner_api.errors import InvalidDocumentError, UnknownRunsError
from servtuner_api.ids import new_ulid
from servtuner_api.models import Plan, SchemaValidationError, validate
from servtuner_api.planner import Planner
from servtuner_api.records.base import utc_now
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.planning import MAX_SCENARIO_NAME, MAX_SCENARIO_NOTE

SCENARIO_BODY_SHAPE = (
    "a scenario is an object with name, scenario (a POST /plans body) and optionally note"
)


PLAN_FIELDS = {"request", "run_ids", "payload_run_ids"}


PLAN_BODY_SHAPE = (
    'the body must be {"request": <plan request>, "run_ids": [...], "payload_run_ids": [...]}'
)


def _run_ids(value: object, field: str, *, allow_empty: bool) -> list[str]:
    if not isinstance(value, list) or not all(isinstance(run_id, str) for run_id in value):
        raise InvalidDocumentError([f"{field} must be a list of run ids"])
    if not value and not allow_empty:
        raise InvalidDocumentError([f"{field} must not be empty"])
    return [str(run_id) for run_id in value]


class ScenarioService:
    """Plans and saved scenarios: bodies checked once, plans
    recomputed."""

    def __init__(self, repository: RunRepository, store: BundleStore, planner: Planner) -> None:
        self._repository = repository
        self._store = store
        self._planner = planner

    def plan(self, raw_body: bytes) -> Plan:
        """Validate a POST /plans body, plan it with bench, and return plan.json."""
        document, runs, run_ids, payload_run_ids = self._checked_plan_body(
            parse_json_document(raw_body)
        )

        def keys(ids: list[str]) -> list[str]:
            return [
                bundle_key(self._store, run) for run_id in ids if (run := runs[run_id]) is not None
            ]

        result = self._planner.plan(document["request"], keys(run_ids), keys(payload_run_ids))
        validate("plan", result)
        return Plan.model_validate(result)

    def save(self, raw_body: bytes) -> ScenarioRow:
        """Check a POST /scenarios body as a plan body would be checked, then keep it."""
        document = parse_json_document(raw_body)
        if not isinstance(document, dict) or not {"name", "scenario"} <= set(document) <= {
            "name",
            "note",
            "scenario",
        }:
            raise InvalidDocumentError([SCENARIO_BODY_SHAPE])
        name, note = document["name"], document.get("note")
        if not isinstance(name, str) or not name.strip() or len(name) > MAX_SCENARIO_NAME:
            raise InvalidDocumentError([f"name: a string of 1 to {MAX_SCENARIO_NAME} characters"])
        if note is not None and (not isinstance(note, str) or len(note) > MAX_SCENARIO_NOTE):
            raise InvalidDocumentError(
                [f"note: a string of at most {MAX_SCENARIO_NOTE} characters"]
            )
        body, _, _, _ = self._checked_plan_body(document["scenario"])
        row = ScenarioRow(
            id=new_ulid(), name=name.strip(), note=note, created_at=utc_now(), body=body
        )
        self._repository.insert_scenario(row)
        return row

    def get(self, scenario_id: str) -> ScenarioRow | None:
        return self._repository.get_scenario(scenario_id)

    def list_all(self) -> list[ScenarioRow]:
        return self._repository.list_scenarios()

    def delete(self, scenario_id: str) -> bool:
        return self._repository.delete_scenario(scenario_id)

    def _checked_plan_body(
        self, document: object
    ) -> tuple[dict[str, Any], dict[str, RunRow | None], list[str], list[str]]:
        """A plan body's shape, its request against the schema, and its runs, which must exist."""
        if (
            not isinstance(document, dict)
            or not {"request", "run_ids"} <= set(document) <= PLAN_FIELDS
        ):
            raise InvalidDocumentError([PLAN_BODY_SHAPE])
        run_ids = _run_ids(document["run_ids"], "run_ids", allow_empty=False)
        payload_run_ids = _run_ids(
            document.get("payload_run_ids", []), "payload_run_ids", allow_empty=True
        )
        try:
            validate("plan-request", document["request"])
        except SchemaValidationError as error:
            raise InvalidDocumentError(
                [f"request: {violation}" for violation in error.violations]
            ) from error
        runs = {run_id: self._repository.get(run_id) for run_id in run_ids + payload_run_ids}
        missing = [run_id for run_id, run in runs.items() if run is None]
        if missing:
            raise UnknownRunsError(missing)
        return document, runs, run_ids, payload_run_ids
