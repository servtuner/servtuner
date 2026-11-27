"""Saved targets: keys are write-only; responses never hold one."""

from fastapi import APIRouter, Request, Response, status
from starlette.concurrency import run_in_threadpool

from servtuner_api.resources.targets import (
    CredentialInput,
    SavedTargetCheck,
    TargetCheckView,
    TargetCreate,
    TargetResource,
    UnsavedTargetCheck,
)
from servtuner_api.routes.common import Service, json_body_schema, target_errors

router = APIRouter()


@router.post("/targets/check", openapi_extra=json_body_schema(UnsavedTargetCheck))
async def check_unsaved_target(request: Request, service: Service) -> TargetCheckView:
    """Check an unsaved target block, as New run's YAML has it; its secretRefs resolve
    as a run's would."""
    body = await request.body()
    with target_errors():
        return await run_in_threadpool(service.targets.check_unsaved, body)


@router.get("/targets", response_model_exclude_none=True)
def list_targets(service: Service) -> list[TargetResource]:
    """Saved targets by name, each with its credential status (never a value)."""
    return [service.targets.resource(row) for row in service.targets.list_targets()]


@router.post(
    "/targets",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(TargetCreate),
)
async def create_target(request: Request, service: Service) -> TargetResource:
    """Save a target block under a name. Keys are set separately (credentials)."""
    body = await request.body()
    with target_errors():
        return service.targets.resource(service.targets.create(body))


@router.get("/targets/{target_id}", response_model_exclude_none=True)
def get_target(target_id: str, service: Service) -> TargetResource:
    with target_errors():
        return service.targets.resource(service.targets.get(target_id))


@router.put(
    "/targets/{target_id}",
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(TargetCreate),
)
async def update_target(target_id: str, request: Request, service: Service) -> TargetResource:
    """Replace name, description and target block; the keys stay as they are."""
    body = await request.body()
    with target_errors():
        return service.targets.resource(service.targets.update(target_id, body))


@router.delete("/targets/{target_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_target(target_id: str, service: Service) -> Response:
    """Delete the target and the Secrets servtuner manages for it. Past runs keep their copy."""
    with target_errors():
        service.targets.delete(target_id)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.put(
    "/targets/{target_id}/credentials/{slot}",
    status_code=status.HTTP_204_NO_CONTENT,
    openapi_extra=json_body_schema(CredentialInput),
)
async def set_target_credential(
    target_id: str, slot: str, request: Request, service: Service
) -> Response:
    """Set or replace a key. Write-only: the response has no body, and no route ever
    returns the value."""
    body = await request.body()
    with target_errors():
        service.targets.set_credential(target_id, slot, body)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.delete("/targets/{target_id}/credentials/{slot}", status_code=status.HTTP_204_NO_CONTENT)
def clear_target_credential(target_id: str, slot: str, service: Service) -> Response:
    with target_errors():
        service.targets.clear_credential(target_id, slot)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.post("/targets/{target_id}/check", openapi_extra=json_body_schema(SavedTargetCheck))
async def check_saved_target(target_id: str, request: Request, service: Service) -> TargetCheckView:
    """Check a saved target with its stored keys, or a one-time api_key that is never
    stored."""
    body = await request.body()
    with target_errors():
        return await run_in_threadpool(service.targets.check_saved, target_id, body)
