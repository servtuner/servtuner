"""Runs: create, list, read, cancel, delete, bundle files, events, analysis outputs and
follow-ups (api/CLAUDE.md "Endpoints")."""

import re
from pathlib import PurePosixPath
from typing import Annotated

from fastapi import APIRouter, HTTPException, Query, Request, Response, status
from fastapi.responses import RedirectResponse, StreamingResponse
from pydantic import BaseModel

from servtuner_api.bundles import BundlePathError
from servtuner_api.deletion import DeleteBlockedError
from servtuner_api.errors import (
    FollowupError,
    ForeignStoreError,
    InvalidBundleFileError,
    InvalidDocumentError,
    InvalidRunTokenError,
    NotAwaitingError,
)
from servtuner_api.ids import ULID_PATTERN
from servtuner_api.models import (
    AnalysisResult,
    CapacityTable,
    ClientTimeseries,
    Environment,
    Event,
    InFlightChart,
    LatencyHistograms,
    Recommendations,
    RunSpec,
    ServerChart,
    TargetSnapshot,
)
from servtuner_api.records.base import RunFilters, RunStatus
from servtuner_api.resources.runs import (
    BundleFileResource,
    DeleteImpactResource,
    FollowupCreate,
    FollowupResource,
    RunDetail,
    RunResource,
    event_from_row,
)
from servtuner_api.routes.common import (
    Service,
    blocked,
    json_body_schema,
    run_not_found,
    run_or_404,
    unprocessable,
)
from servtuner_api.service import RunService

DEFAULT_PAGE_SIZE = 100
MAX_PAGE_SIZE = 500
BEARER_PATTERN = re.compile(r"^Bearer (\S+)$")
# Media types of bundle files by suffix; anything else is octet-stream.
MEDIA_TYPES = {
    ".json": "application/json",
    ".parquet": "application/vnd.apache.parquet",
    ".yaml": "application/yaml",
    ".csv": "text/csv",
    ".md": "text/markdown",
}

router = APIRouter()


def _bearer_token(request: Request) -> str:
    match = BEARER_PATTERN.match(request.headers.get("Authorization", ""))
    if match is None:
        raise HTTPException(
            status.HTTP_401_UNAUTHORIZED, "missing bearer token", {"WWW-Authenticate": "Bearer"}
        )
    return match.group(1)


def _label_filter(label: str | None) -> tuple[str, str] | None:
    if label is None:
        return None
    key, separator, value = label.partition("=")
    if not separator or not key:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "label must be key=value")
    return key, value


def _media_type(path: str) -> str:
    return MEDIA_TYPES.get(PurePosixPath(path).suffix, "application/octet-stream")


def _run_document[T: BaseModel](
    service: RunService, run_id: str, path: str, schema_name: str, model: type[T]
) -> T:
    run = run_or_404(service, run_id)
    try:
        document = service.read_document(run, path, schema_name, model)
    except ForeignStoreError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    except InvalidBundleFileError as error:
        raise HTTPException(status.HTTP_502_BAD_GATEWAY, str(error)) from error
    if document is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"run {run_id} has no {path} yet")
    return document


@router.post(
    "/runs",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(RunSpec),
)
async def create_run(request: Request, service: Service) -> RunResource:
    try:
        run = service.create_run(await request.body())
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    return service.resource(run)


@router.get("/runs", response_model_exclude_none=True)
def list_runs(
    service: Service,
    experiment: str | None = None,
    target_type: str | None = None,
    run_status: Annotated[RunStatus | None, Query(alias="status")] = None,
    label: Annotated[str | None, Query(description="key=value")] = None,
    limit: Annotated[int, Query(ge=1, le=MAX_PAGE_SIZE)] = DEFAULT_PAGE_SIZE,
) -> list[RunResource]:
    filters = RunFilters(experiment, target_type, run_status, _label_filter(label))
    return service.resources(service.list_runs(filters, limit))


@router.get("/runs/{run_id}", response_model_exclude_none=True)
def get_run(run_id: str, service: Service) -> RunDetail:
    run = service.get_run(run_id) if ULID_PATTERN.match(run_id) else None
    if run is None:
        raise run_not_found(run_id)
    resource = service.resource(run)
    return RunDetail(**resource.model_dump(), summary=service.read_summary(run))


@router.post("/runs/{run_id}/cancel", response_model_exclude_none=True)
def cancel_run(run_id: str, service: Service) -> RunResource:
    """Stop a run; a finished run is returned unchanged."""
    run = service.cancel_run(run_id) if ULID_PATTERN.match(run_id) else None
    if run is None:
        raise run_not_found(run_id)
    return service.resource(run)


@router.get("/runs/{run_id}/delete-impact")
def run_delete_impact(run_id: str, service: Service) -> DeleteImpactResource:
    """What deleting the run would remove, and what blocks it; deletes nothing."""
    return DeleteImpactResource.of(service.run_delete_impact(run_or_404(service, run_id)))


@router.delete("/runs/{run_id}")
def delete_run(run_id: str, service: Service) -> DeleteImpactResource:
    """Delete a run, its bundle and the analyses that included it; only ever a user's request
    . 409 with the blocks while something needs it."""
    run = run_or_404(service, run_id)
    try:
        return DeleteImpactResource.of(service.delete_run(run))
    except DeleteBlockedError as error:
        raise blocked("the run cannot be deleted", error) from error


@router.get("/runs/{run_id}/files")
def list_files(run_id: str, service: Service) -> list[BundleFileResource]:
    run = run_or_404(service, run_id)
    try:
        files = service.list_files(run)
    except ForeignStoreError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    return [BundleFileResource(path=file.path, size_bytes=file.size_bytes) for file in files]


@router.get(
    "/runs/{run_id}/files/{path:path}",
    response_class=StreamingResponse,
    responses={307: {"description": "Redirect to a presigned download (storage.s3.presign)"}},
)
def get_file(run_id: str, path: str, service: Service) -> Response:
    """A bundle file: proxied in chunks, or a redirect to a presigned URL."""
    run = run_or_404(service, run_id)
    try:
        opened = service.open_file(run, path)
    except BundlePathError as error:
        raise HTTPException(status.HTTP_400_BAD_REQUEST, str(error)) from error
    except ForeignStoreError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    if opened is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"run {run_id} has no file {path}")
    if isinstance(opened, str):
        return RedirectResponse(opened, status_code=status.HTTP_307_TEMPORARY_REDIRECT)
    return StreamingResponse(opened, media_type=_media_type(path))


@router.get("/runs/{run_id}/events", response_model_exclude_none=True)
def list_events(run_id: str, service: Service) -> list[Event]:
    if not ULID_PATTERN.match(run_id) or service.get_run(run_id) is None:
        raise run_not_found(run_id)
    return [event_from_row(row) for row in service.list_events(run_id)]


@router.get("/runs/{run_id}/datasheet", response_class=Response)
def get_run_datasheet(run_id: str, request: Request, service: Service) -> Response:
    """bench's copy of the price book's uploaded datasheet for its run: run token only.
    404 when the price book has no upload."""
    token = _bearer_token(request)
    try:
        content = service.run_datasheet(run_id, token)
    except InvalidRunTokenError as error:
        raise HTTPException(
            status.HTTP_401_UNAUTHORIZED, "invalid run token", {"WWW-Authenticate": "Bearer"}
        ) from error
    if content is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "the price book has no uploaded datasheet")
    return Response(content=content, media_type="application/json")


@router.post(
    "/runs/{run_id}/events",
    status_code=status.HTTP_202_ACCEPTED,
    openapi_extra=json_body_schema(Event),
)
async def post_event(run_id: str, request: Request, service: Service) -> Response:
    token = _bearer_token(request)
    try:
        service.record_event(run_id, token, await request.body())
    except InvalidRunTokenError as error:
        raise HTTPException(
            status.HTTP_401_UNAUTHORIZED,
            "invalid run token",
            {"WWW-Authenticate": "Bearer"},
        ) from error
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    return Response(status_code=status.HTTP_202_ACCEPTED)


@router.get("/runs/{run_id}/analysis", response_model_exclude_none=True)
def get_analysis(run_id: str, service: Service) -> AnalysisResult:
    return _run_document(service, run_id, "analysis.json", "analysis", AnalysisResult)


@router.get("/runs/{run_id}/recommendations", response_model_exclude_none=True)
def get_recommendations(run_id: str, service: Service) -> Recommendations:
    return _run_document(
        service, run_id, "recommendations.json", "recommendations", Recommendations
    )


@router.get("/runs/{run_id}/capacity", response_model_exclude_none=True)
def get_capacity(run_id: str, service: Service) -> CapacityTable:
    return _run_document(service, run_id, "capacity.json", "capacity", CapacityTable)


@router.get("/runs/{run_id}/charts/client_timeseries", response_model_exclude_none=True)
def get_client_timeseries(run_id: str, service: Service) -> ClientTimeseries:
    path = "charts/client_timeseries.json"
    return _run_document(service, run_id, path, "chart-client-timeseries", ClientTimeseries)


@router.get("/runs/{run_id}/environment", response_model_exclude_none=True)
def get_environment(run_id: str, service: Service) -> Environment:
    """What ran the benchmark: bench, engine, transport per hop, captured knobs."""
    return _run_document(service, run_id, "environment.json", "environment", Environment)


@router.get("/runs/{run_id}/snapshot", response_model_exclude_none=True)
def get_snapshot(
    run_id: str, service: Service, phase: Annotated[str, Query(pattern="^(pre|post)$")] = "pre"
) -> TargetSnapshot:
    """The target as captured before (pre) or after (post) the load."""
    path = f"target/snapshot-{phase}.json"
    return _run_document(service, run_id, path, "target-snapshot", TargetSnapshot)


@router.get("/runs/{run_id}/charts/inflight", response_model_exclude_none=True)
def get_inflight(run_id: str, service: Service) -> InFlightChart:
    path = "charts/inflight.json"
    return _run_document(service, run_id, path, "chart-inflight", InFlightChart)


@router.get("/runs/{run_id}/charts/server", response_model_exclude_none=True)
def get_server_chart(run_id: str, service: Service) -> ServerChart:
    return _run_document(service, run_id, "charts/server.json", "chart-server", ServerChart)


@router.get("/runs/{run_id}/charts/histograms", response_model_exclude_none=True)
def get_histograms(run_id: str, service: Service) -> LatencyHistograms:
    path = "charts/histograms.json"
    return _run_document(service, run_id, path, "chart-histograms", LatencyHistograms)


@router.post(
    "/runs/{run_id}/followup",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
)
def create_followup(run_id: str, body: FollowupCreate, service: Service) -> FollowupResource:
    run = run_or_404(service, run_id)
    try:
        runs, recommendation, patch = service.create_followup(
            run, body.recommendation_id, body.repeats, body.experiment
        )
    except FollowupError as error:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(error)) from error
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    except ForeignStoreError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    except InvalidBundleFileError as error:
        raise HTTPException(status.HTTP_502_BAD_GATEWAY, str(error)) from error
    value = recommendation.recommended.root if recommendation.recommended else None
    assert value is not None  # create_followup refuses a recommendation without one
    return FollowupResource(
        runs=service.resources(runs),
        knob=recommendation.subject.name,
        value=value,
        patch=patch,
    )


@router.post("/runs/{run_id}/confirm-applied", response_model_exclude_none=True)
def confirm_applied(run_id: str, service: Service) -> RunResource:
    """The change this follow-up run tests is applied: launch it."""
    run_or_404(service, run_id)
    try:
        return service.resource(service.confirm_applied(run_id))
    except NotAwaitingError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from error
