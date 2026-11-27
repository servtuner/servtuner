"""Managed targets: vLLMs servtuner deploys itself."""

from typing import Annotated

from fastapi import APIRouter, Query, Request, Response, status
from starlette.concurrency import run_in_threadpool

from servtuner_api.deployer.boot import LOG_TAIL_LINES, MAX_LOG_TAIL_LINES
from servtuner_api.managed_targets.views import (
    BootProbeRequest,
    BootProbeView,
    DeployRequest,
    LauncherCheckView,
    LogView,
    ManagedTargetCreate,
    ManagedTargetResource,
    ReadinessView,
)
from servtuner_api.resources.targets import CredentialInput
from servtuner_api.routes.common import (
    Service,
    deploy_errors,
    json_body_schema,
    session_errors,
    target_errors,
)
from servtuner_api.service import RunService

router = APIRouter()


def _refuse_while_tuning(service: RunService, target_id: str) -> None:
    """409 while an active session uses the managed target: a user's change would break it."""
    with session_errors():
        service.tuning.refuse_if_tuning(target_id)


@router.get("/managed-targets", response_model_exclude_none=True)
def list_managed_targets(service: Service) -> list[ManagedTargetResource]:
    return [service.managed_targets.resource(row) for row in service.managed_targets.list_targets()]


@router.post(
    "/managed-targets",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(ManagedTargetCreate),
)
async def create_managed_target(request: Request, service: Service) -> ManagedTargetResource:
    """Store a vLLM template in a tuning namespace (the chart's rbac.tuning.namespaces)."""
    body = await request.body()
    with target_errors():
        return service.managed_targets.resource(service.managed_targets.create(body))


@router.get("/managed-targets/{target_id}", response_model_exclude_none=True)
def get_managed_target(target_id: str, service: Service) -> ManagedTargetResource:
    with target_errors():
        return service.managed_targets.resource(service.managed_targets.get(target_id))


@router.put(
    "/managed-targets/{target_id}",
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(ManagedTargetCreate),
)
async def update_managed_target(
    target_id: str, request: Request, service: Service
) -> ManagedTargetResource:
    body = await request.body()
    _refuse_while_tuning(service, target_id)
    with target_errors():
        return service.managed_targets.resource(service.managed_targets.update(target_id, body))


@router.delete("/managed-targets/{target_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_managed_target(target_id: str, service: Service) -> Response:
    """Tear down what servtuner deployed for it (the model cache stays), then forget it."""
    _refuse_while_tuning(service, target_id)
    with target_errors(), deploy_errors():
        service.managed_targets.delete(target_id)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.put(
    "/managed-targets/{target_id}/credentials/{slot}",
    status_code=status.HTTP_204_NO_CONTENT,
    openapi_extra=json_body_schema(CredentialInput),
)
async def set_managed_target_credential(
    target_id: str, slot: str, request: Request, service: Service
) -> Response:
    """A host target's launcher_token or api_key: a pasted value servtuner keeps in a Secret it
    owns, or a Secret you name. Write-only: no route ever returns the value."""
    body = await request.body()
    _refuse_while_tuning(service, target_id)
    with target_errors():
        service.managed_targets.set_credential(target_id, slot, body)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.delete(
    "/managed-targets/{target_id}/credentials/{slot}", status_code=status.HTTP_204_NO_CONTENT
)
def clear_managed_target_credential(target_id: str, slot: str, service: Service) -> Response:
    _refuse_while_tuning(service, target_id)
    with target_errors():
        service.managed_targets.clear_credential(target_id, slot)
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.post(
    "/managed-targets/{target_id}/deploy",
    status_code=status.HTTP_202_ACCEPTED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(DeployRequest),
)
async def deploy_managed_target(
    target_id: str, request: Request, service: Service
) -> ManagedTargetResource:
    """Apply the target with these vLLM knobs. Poll readiness before starting a run."""
    body = await request.body()
    _refuse_while_tuning(service, target_id)
    with target_errors(), deploy_errors():
        row = await run_in_threadpool(service.managed_targets.deploy, target_id, body)
    return service.managed_targets.resource(row)


@router.get("/managed-targets/{target_id}/readiness", response_model_exclude_none=True)
async def managed_target_readiness(target_id: str, service: Service) -> ReadinessView:
    """One readiness poll: pending, ready, infeasible (a configuration result) or failed."""
    with target_errors(), deploy_errors():
        return await run_in_threadpool(service.managed_targets.readiness, target_id)


@router.post("/managed-targets/{target_id}/check-launcher", response_model_exclude_none=True)
async def check_managed_target_launcher(target_id: str, service: Service) -> LauncherCheckView:
    """A host target's launcher: reached, token accepted, and what vLLM is doing there.
    Read-only, so it is allowed while a session tunes the target."""
    with target_errors(), deploy_errors():
        return await run_in_threadpool(service.managed_targets.check_launcher, target_id)


@router.get("/managed-targets/{target_id}/logs", response_model_exclude_none=True)
async def managed_target_logs(
    target_id: str,
    service: Service,
    tail_lines: Annotated[int, Query(ge=1, le=MAX_LOG_TAIL_LINES)] = LOG_TAIL_LINES,
) -> LogView:
    """The end of vLLM's log: the newest pod's (a crash-looping one's last exit), or the host
    launcher's. Read-only, so it is allowed while a session tunes the target."""
    with target_errors(), deploy_errors():
        return await run_in_threadpool(service.managed_targets.logs, target_id, tail_lines)


@router.post(
    "/managed-targets/{target_id}/boot-probe",
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(BootProbeRequest),
)
async def boot_probe_managed_target(
    target_id: str, request: Request, service: Service
) -> BootProbeView:
    """Poll the deployed configuration as a boot probe: its KV capacity, or why it did not
    boot (a tuningBootResult for servtuner propose). No result while it is still starting."""
    body = await request.body()
    with target_errors(), deploy_errors():
        return await run_in_threadpool(service.managed_targets.boot_probe, target_id, body)


@router.post("/managed-targets/{target_id}/scale-down", status_code=status.HTTP_204_NO_CONTENT)
def scale_down_managed_target(target_id: str, service: Service) -> Response:
    """Free the GPU: the Deployment goes to 0 replicas; the objects and cache stay."""
    _refuse_while_tuning(service, target_id)
    with target_errors(), deploy_errors():
        service.managed_targets.scale_down(target_id)
    return Response(status_code=status.HTTP_204_NO_CONTENT)
