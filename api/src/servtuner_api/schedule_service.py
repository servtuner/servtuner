"""The schedule service: create, change, trigger and judge schedules.

The rules (slots, labels, verdicts) are in ``schedules.py``; runs launch through RunService.
"""

import json
from collections.abc import Callable
from datetime import datetime

from servtuner_api.database import RunRow, ScheduleRow
from servtuner_api.errors import InvalidDocumentError, ScheduleBusyError, UnknownRunsError
from servtuner_api.ids import new_ulid
from servtuner_api.indexing import RUN_STAGE, VALUE
from servtuner_api.models import SchemaValidationError, parse_run_spec
from servtuner_api.records.base import RunFilters, RunStatus, is_final, utc_now
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.schedules import (
    SECONDS_PER_HOUR,
    ScheduleCreate,
    ScheduleDetail,
    ScheduledRunResource,
    ScheduleResource,
    ScheduleUpdate,
)
from servtuner_api.schedules import SCHEDULE_LABEL, Measurement, first_slot, labelled_spec, verdict

MAX_SCHEDULE_HISTORY = 200  # a schedule's history lists at most this many of its newest runs


def measurements(repository: RunRepository, run_ids: list[str]) -> dict[str, Measurement]:
    values: dict[tuple[str, str], float] = {}
    for row in repository.list_metrics(run_ids):
        if row.stage == RUN_STAGE and row.stat == VALUE:
            values[row.run_id, row.metric] = row.value
    return {
        run_id: Measurement(
            slo_max_rps=values.get((run_id, "slo_max_rps")),
            is_excluded=values.get((run_id, "is_excluded_from_comparison"), 0.0) > 0,
        )
        for run_id in run_ids
    }


class ScheduleService:
    """Schedules: create, change, trigger and judge; runs launch through
    RunService."""

    def __init__(self, repository: RunRepository, create_run: Callable[[bytes], RunRow]) -> None:
        self._repository = repository
        self._create_run = create_run

    def run_due(self, now: datetime) -> list[str]:
        """Launch every schedule that is due at ``now``; return the new runs' ids."""
        return [
            run_id
            for schedule_id, spec_raw in self._repository.claim_due_schedules(now)
            if (run_id := self._launch(schedule_id, spec_raw)) is not None
        ]

    def _launch(self, schedule_id: str, spec_raw: bytes) -> str | None:
        try:
            run = self._create_run(labelled_spec(spec_raw, schedule_id))
        except InvalidDocumentError as error:  # e.g. the schema moved on since it was saved
            note = "the RunSpec is no longer valid: " + "; ".join(error.violations)
            self._repository.update_schedule(schedule_id, last_note=note)
            return None
        self._repository.update_schedule(schedule_id, last_run_id=run.id, last_note=None)
        return run.id

    def create(self, body: ScheduleCreate) -> ScheduleRow:
        spec_raw = json.dumps(body.spec).encode()
        try:
            parse_run_spec(body.spec)
        except SchemaValidationError as error:
            raise InvalidDocumentError([f"spec: {v}" for v in error.violations]) from error
        self._check_baseline(body.baseline_run_id)
        now = utc_now()
        every_seconds = round(body.every_hours * SECONDS_PER_HOUR)
        start_at = body.start_at or now
        row = ScheduleRow(
            id=new_ulid(),
            name=body.name.strip(),
            spec_raw=spec_raw,
            every_seconds=every_seconds,
            start_at=start_at,
            next_run_at=start_at if start_at <= now else first_slot(start_at, every_seconds, now),
            is_enabled=body.is_enabled,
            baseline_run_id=body.baseline_run_id,
            regression_tolerance=body.regression_tolerance,
            created_at=now,
        )
        self._repository.insert_schedule(row)
        return row

    def update(self, schedule_id: str, body: ScheduleUpdate) -> ScheduleRow | None:
        changes = {name: getattr(body, name) for name in body.model_fields_set}
        for name in ("name", "is_enabled", "regression_tolerance"):
            if name in changes and changes[name] is None:
                raise InvalidDocumentError([f"{name}: may not be null"])
        if "baseline_run_id" in changes:
            self._check_baseline(changes["baseline_run_id"])
        if isinstance(changes.get("name"), str):
            changes["name"] = changes["name"].strip()
        return self._repository.update_schedule(schedule_id, **changes)

    def _check_baseline(self, run_id: str | None) -> None:
        if run_id is not None and self._repository.get(run_id) is None:
            raise UnknownRunsError([run_id])

    def trigger(self, schedule_id: str) -> RunRow | None:
        """Launch the schedule now, outside its slots; None when there is no such schedule."""
        schedule = self._repository.get_schedule(schedule_id)
        if schedule is None:
            return None
        last = self._repository.get(schedule.last_run_id) if schedule.last_run_id else None
        if last is not None and not is_final(last.status):
            raise ScheduleBusyError(f"run {last.id} of this schedule is still {last.status}")
        run_id = self._launch(schedule.id, schedule.spec_raw)
        if run_id is None:
            fresh = self._repository.get_schedule(schedule_id)
            raise InvalidDocumentError([fresh.last_note if fresh and fresh.last_note else "?"])
        return self._repository.get(run_id)

    def get(self, schedule_id: str) -> ScheduleRow | None:
        return self._repository.get_schedule(schedule_id)

    def list_all(self) -> list[ScheduleRow]:
        return self._repository.list_schedules()

    def delete(self, schedule_id: str) -> bool:
        """Delete the schedule; its runs stay, with their label."""
        return self._repository.delete_schedule(schedule_id)

    def detail(self, schedule: ScheduleRow) -> ScheduleDetail:
        """The schedule's runs, oldest first, each judged against the baseline (schedules.py)."""
        filters = RunFilters(label=(SCHEDULE_LABEL, schedule.id))
        runs = list(reversed(self._repository.list_runs(filters, MAX_SCHEDULE_HISTORY)))
        ids = [run.id for run in runs]
        if schedule.baseline_run_id and schedule.baseline_run_id not in ids:
            ids.append(schedule.baseline_run_id)
        measured = measurements(self._repository, ids)
        statuses = {run.id: run.status for run in runs}
        baseline_id = schedule.baseline_run_id or next(
            (
                run.id
                for run in runs
                if run.status == RunStatus.SUCCEEDED and measured[run.id].is_usable
            ),
            None,
        )
        baseline = measured[baseline_id].slo_max_rps if baseline_id in measured else None
        history = []
        for run in runs:
            judged, change = verdict(
                statuses[run.id],
                is_final(run.status),
                measured[run.id],
                baseline,
                run.id == baseline_id,
                schedule.regression_tolerance,
            )
            history.append(
                ScheduledRunResource(
                    run_id=run.id,
                    status=RunStatus(run.status),
                    created_at=run.created_at,
                    slo_max_rps=measured[run.id].slo_max_rps,
                    verdict=judged,
                    change=change,
                )
            )
        return ScheduleDetail(
            **ScheduleResource.from_row(schedule).model_dump(),
            effective_baseline_run_id=baseline_id,
            baseline_slo_max_rps=baseline,
            runs=history,
        )
