"""Scheduled regression runs."""

import json
from datetime import datetime
from typing import Any

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import ScheduleRow
from servtuner_api.records.base import RunStatus
from servtuner_api.schedules import (
    DEFAULT_REGRESSION_TOLERANCE,
    MAX_EVERY_SECONDS,
    MIN_EVERY_SECONDS,
    Verdict,
)

SECONDS_PER_HOUR = 3600


MAX_SCHEDULE_NAME = 200


class ScheduleCreate(BaseModel):
    """POST /schedules: launch a RunSpec every ``every_hours`` from ``start_at`` (default now)."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=MAX_SCHEDULE_NAME)
    spec: dict[str, Any] = Field(description="A RunSpec, checked like a POST /runs body")
    every_hours: float = Field(
        ge=MIN_EVERY_SECONDS / SECONDS_PER_HOUR, le=MAX_EVERY_SECONDS / SECONDS_PER_HOUR
    )
    start_at: datetime | None = None
    baseline_run_id: str | None = None
    regression_tolerance: float = Field(default=DEFAULT_REGRESSION_TOLERANCE, gt=0, le=0.5)
    is_enabled: bool = True


class ScheduleUpdate(BaseModel):
    """PATCH /schedules/{id}: only the fields given change; baseline_run_id null clears it."""

    model_config = ConfigDict(extra="forbid")

    name: str | None = Field(default=None, min_length=1, max_length=MAX_SCHEDULE_NAME)
    is_enabled: bool | None = None
    baseline_run_id: str | None = None
    regression_tolerance: float | None = Field(default=None, gt=0, le=0.5)


class ScheduleResource(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    spec: dict[str, Any]
    every_hours: float
    start_at: datetime
    next_run_at: datetime
    is_enabled: bool
    baseline_run_id: str | None = None
    regression_tolerance: float
    last_run_id: str | None = None
    last_note: str | None = Field(
        default=None, description="Why the last due slot launched nothing"
    )
    created_at: datetime

    @classmethod
    def from_row(cls, row: ScheduleRow) -> ScheduleResource:
        return cls(
            id=row.id,
            name=row.name,
            spec=json.loads(row.spec_raw),
            every_hours=row.every_seconds / SECONDS_PER_HOUR,
            start_at=row.start_at,
            next_run_at=row.next_run_at,
            is_enabled=row.is_enabled,
            baseline_run_id=row.baseline_run_id,
            regression_tolerance=row.regression_tolerance,
            last_run_id=row.last_run_id,
            last_note=row.last_note,
            created_at=row.created_at,
        )


class ScheduledRunResource(BaseModel):
    """One run of a schedule, with its SLO-max against the baseline's."""

    model_config = ConfigDict(extra="forbid")

    run_id: str
    status: RunStatus
    created_at: datetime
    slo_max_rps: float | None = None
    verdict: Verdict
    change: float | None = Field(
        default=None,
        description="SLO-max RPS against the baseline's, as a fraction (-0.2 is 20% lower)",
    )


class ScheduleDetail(ScheduleResource):
    """A schedule with its runs, oldest first, and the baseline they are judged against."""

    effective_baseline_run_id: str | None = None
    baseline_slo_max_rps: float | None = None
    runs: list[ScheduledRunResource]
