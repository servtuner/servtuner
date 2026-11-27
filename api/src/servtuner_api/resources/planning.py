"""Plan bodies and saved scenarios."""

from datetime import datetime

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import ScenarioRow
from servtuner_api.models import PlanRequest


class PlanCreate(BaseModel):
    """POST /plans: a plan request, the runs whose capacity models to plan with, and
    optionally a payload-size series that only fits m(s)."""

    model_config = ConfigDict(extra="forbid")

    request: PlanRequest
    run_ids: list[str]
    payload_run_ids: list[str] = []


MAX_SCENARIO_NAME = 200


MAX_SCENARIO_NOTE = 4000


class ScenarioCreate(BaseModel):
    """POST /scenarios: save a plan body (request, overrides and runs) under a name."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=MAX_SCENARIO_NAME)
    note: str | None = Field(default=None, max_length=MAX_SCENARIO_NOTE)
    scenario: PlanCreate


class ScenarioResource(BaseModel):
    """A saved scenario. Its plan is recomputed with POST /scenarios/evaluate."""

    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    note: str | None = None
    created_at: datetime
    scenario: PlanCreate

    @classmethod
    def from_row(cls, row: ScenarioRow) -> ScenarioResource:
        return cls(
            id=row.id,
            name=row.name,
            note=row.note,
            created_at=row.created_at,
            scenario=PlanCreate.model_validate(row.body),
        )
