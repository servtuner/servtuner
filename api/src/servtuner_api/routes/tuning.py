"""Tuning sessions: search a managed target's vLLM flags for a preset and SLOs."""

from fastapi import APIRouter, Request, status
from starlette.concurrency import run_in_threadpool

from servtuner_api.routes.common import Service, json_body_schema, session_errors, target_errors
from servtuner_api.targets.common import parse_body
from servtuner_api.tuning.models import SessionCreate, SessionResource

router = APIRouter()


@router.post(
    "/tuning-sessions",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(SessionCreate),
)
async def create_tuning_session(request: Request, service: Service) -> SessionResource:
    """Start a session. One at a time (409 while another is active): they share the GPU."""
    raw = await request.body()
    with target_errors(), session_errors():
        body = parse_body(raw, SessionCreate)
        row = await run_in_threadpool(service.tuning.create, body)
    return SessionResource.from_row(row)


@router.get("/tuning-sessions", response_model_exclude_none=True)
def list_tuning_sessions(service: Service) -> list[SessionResource]:
    return [SessionResource.from_row(row) for row in service.tuning.list_sessions()]


@router.get("/tuning-sessions/{session_id}", response_model_exclude_none=True)
def get_tuning_session(session_id: str, service: Service) -> SessionResource:
    with session_errors():
        return SessionResource.from_row(service.tuning.get(session_id))


@router.post("/tuning-sessions/{session_id}/approve", response_model_exclude_none=True)
def approve_tuning_step(session_id: str, service: Service) -> SessionResource:
    """Manual mode: run the step the session proposed."""
    with session_errors():
        return SessionResource.from_row(service.tuning.approve(session_id))


@router.post("/tuning-sessions/{session_id}/cancel", response_model_exclude_none=True)
def cancel_tuning_session(session_id: str, service: Service) -> SessionResource:
    """Stop issuing steps and free the GPU; a run already started finishes on its own."""
    with session_errors():
        return SessionResource.from_row(service.tuning.cancel(session_id))
