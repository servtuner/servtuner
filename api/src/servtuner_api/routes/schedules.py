"""Scheduled regression runs (api/CLAUDE.md "Scheduled regression runs")."""

from fastapi import APIRouter, HTTPException, Response, status

from servtuner_api.database import ScheduleRow
from servtuner_api.errors import InvalidDocumentError, ScheduleBusyError, UnknownRunsError
from servtuner_api.ids import ULID_PATTERN
from servtuner_api.resources.runs import RunResource
from servtuner_api.resources.schedules import (
    ScheduleCreate,
    ScheduleDetail,
    ScheduleResource,
    ScheduleUpdate,
)
from servtuner_api.routes.common import Service, unprocessable
from servtuner_api.service import RunService

router = APIRouter()


def found(service: RunService, schedule_id: str) -> ScheduleRow:
    row = service.schedules.get(schedule_id) if ULID_PATTERN.match(schedule_id) else None
    if row is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"no schedule {schedule_id}")
    return row


@router.post("/schedules", status_code=status.HTTP_201_CREATED, response_model_exclude_none=True)
def create_schedule(body: ScheduleCreate, service: Service) -> ScheduleResource:
    """Keep a RunSpec to launch every every_hours; its first run is due at start_at."""
    try:
        row = service.schedules.create(body)
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    except UnknownRunsError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    return ScheduleResource.from_row(row)


@router.get("/schedules", response_model_exclude_none=True)
def list_schedules(service: Service) -> list[ScheduleResource]:
    """Schedules, newest first."""
    return [ScheduleResource.from_row(row) for row in service.schedules.list_all()]


@router.get("/schedules/{schedule_id}", response_model_exclude_none=True)
def get_schedule(schedule_id: str, service: Service) -> ScheduleDetail:
    """The schedule with its runs, each judged against the baseline's SLO-max."""
    return service.schedules.detail(found(service, schedule_id))


@router.patch("/schedules/{schedule_id}", response_model_exclude_none=True)
def update_schedule(schedule_id: str, body: ScheduleUpdate, service: Service) -> ScheduleResource:
    found(service, schedule_id)
    try:
        row = service.schedules.update(schedule_id, body)
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    except UnknownRunsError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    return ScheduleResource.from_row(found(service, schedule_id) if row is None else row)


@router.post(
    "/schedules/{schedule_id}/trigger",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
)
def trigger_schedule(schedule_id: str, service: Service) -> RunResource:
    """Launch the schedule's RunSpec now; 409 while its last run is unfinished."""
    found(service, schedule_id)
    try:
        run = service.schedules.trigger(schedule_id)
    except ScheduleBusyError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from error
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    if run is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"no schedule {schedule_id}")
    return service.resource(run)


@router.delete("/schedules/{schedule_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_schedule(schedule_id: str, service: Service) -> Response:
    """Delete the schedule; its runs are kept."""
    if not service.schedules.delete(found(service, schedule_id).id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"no schedule {schedule_id}")
    return Response(status_code=status.HTTP_204_NO_CONTENT)
