"""Hosts: the machines host targets run on."""

from fastapi import APIRouter, HTTPException, Request, Response, status
from starlette.concurrency import run_in_threadpool

from servtuner_api.hosts import HostCreate, HostInUseError, HostResource
from servtuner_api.routes.common import Service, deploy_errors, json_body_schema, target_errors
from servtuner_api.targets.common import TargetInputError

router = APIRouter()


@router.get("/hosts", response_model_exclude_none=True)
def list_hosts(service: Service) -> list[HostResource]:
    return service.hosts.list_hosts()


@router.post(
    "/hosts",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(HostCreate),
)
async def create_host(request: Request, service: Service) -> HostResource:
    body = await request.body()
    with target_errors():
        return service.hosts.create(body)


@router.get("/hosts/{host_id}", response_model_exclude_none=True)
def get_host(host_id: str, service: Service) -> HostResource:
    with target_errors():
        return service.hosts.get(host_id)


@router.put(
    "/hosts/{host_id}",
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(HostCreate),
)
async def update_host(host_id: str, request: Request, service: Service) -> HostResource:
    body = await request.body()
    with target_errors():
        return service.hosts.update(host_id, body)


@router.delete("/hosts/{host_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_host(host_id: str, service: Service) -> Response:
    """Refused (409) while a managed target names the host."""
    with target_errors():
        try:
            service.hosts.delete(host_id)
        except HostInUseError as error:
            raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from None
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.post("/hosts/{host_id}/read-machine", response_model_exclude_none=True)
async def read_host_machine(host_id: str, service: Service) -> HostResource:
    """Read the machine through the launcher of a host target on it (GET /machine) and keep
    it as the host's hardware. 422 when no host target names the host."""
    with target_errors(), deploy_errors():
        targets = service.hosts.get(host_id).target_ids
        if not targets:
            raise TargetInputError(["no managed target runs on this host: its launcher reads it"])
        machine = await run_in_threadpool(service.managed_targets.machine, targets[0])
        return service.hosts.set_hardware(host_id, machine)
