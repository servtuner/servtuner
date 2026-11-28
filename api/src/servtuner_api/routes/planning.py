"""Plans and scenarios, RunSpec validation, and the
catalogs bench carries: presets, search spaces, knobs."""

from fastapi import APIRouter, HTTPException, Request, Response, status

from servtuner_api.errors import ForeignStoreError, InvalidDocumentError, UnknownRunsError
from servtuner_api.models import KnobCatalog, Plan, SearchSpaceTemplate, WorkloadPreset
from servtuner_api.models.generated.knob_catalog_schema import Server as KnobServer
from servtuner_api.planner import MissingCapacityModelError, PlanError, PlannerUnavailableError
from servtuner_api.resources.planning import PlanCreate, ScenarioCreate, ScenarioResource
from servtuner_api.resources.runs import RunDefaults, SpecValidationResource
from servtuner_api.routes.common import Service, json_body_schema, unprocessable
from servtuner_api.service import RunService
from servtuner_api.specs import ValidatorUnavailableError

router = APIRouter()


async def create_plan(request: Request, service: RunService) -> Plan:
    """POST /plans and POST /scenarios/evaluate: one body, one set of errors."""
    try:
        return service.scenarios.plan(await request.body())
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    except (UnknownRunsError, MissingCapacityModelError, ForeignStoreError) as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    except PlanError as error:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(error)) from error
    except PlannerUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error


@router.post("/plans", response_model_exclude_none=True, openapi_extra=json_body_schema(PlanCreate))
async def plan(request: Request, service: Service) -> Plan:
    """Plan replicas, HPA and totals from the runs' capacity models."""
    return await create_plan(request, service)


@router.post(
    "/scenarios/evaluate",
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(PlanCreate),
)
async def evaluate_scenario(request: Request, service: Service) -> Plan:
    """The workbench's recompute: the same as POST /plans (models, inputs, overrides)."""
    return await create_plan(request, service)


@router.post(
    "/scenarios",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
    openapi_extra=json_body_schema(ScenarioCreate),
)
async def save_scenario(request: Request, service: Service) -> ScenarioResource:
    """Save a scenario under a name; its runs must exist and its request must be valid."""
    try:
        row = service.scenarios.save(await request.body())
    except InvalidDocumentError as error:
        raise unprocessable(error) from error
    except UnknownRunsError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    return ScenarioResource.from_row(row)


@router.get("/scenarios", response_model_exclude_none=True)
def list_scenarios(service: Service) -> list[ScenarioResource]:
    """Saved scenarios, newest first."""
    return [ScenarioResource.from_row(row) for row in service.scenarios.list_all()]


@router.get("/scenarios/{scenario_id}", response_model_exclude_none=True)
def get_scenario(scenario_id: str, service: Service) -> ScenarioResource:
    row = service.scenarios.get(scenario_id)
    if row is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"no scenario {scenario_id}")
    return ScenarioResource.from_row(row)


@router.delete("/scenarios/{scenario_id}", status_code=status.HTTP_204_NO_CONTENT)
def delete_scenario(scenario_id: str, service: Service) -> Response:
    if not service.scenarios.delete(scenario_id):
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"no scenario {scenario_id}")
    return Response(status_code=status.HTTP_204_NO_CONTENT)


@router.post(
    "/specs/validate",
    response_model_exclude_none=True,
    openapi_extra={
        "requestBody": {
            "required": True,
            "content": {"text/plain": {"schema": {"type": "string"}}},
        }
    },
)
async def validate_spec(request: Request, service: Service) -> SpecValidationResource:
    """A dry run of servtuner validate on a RunSpec in YAML or JSON (nothing is stored)."""
    try:
        report = service.validate_spec(await request.body())
    except ValidatorUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error
    return SpecValidationResource(
        valid=report.valid, name=report.name, errors=report.errors, warnings=report.warnings
    )


@router.get("/knobs", response_model_exclude_none=True)
def knob_catalog(service: Service, server: KnobServer = KnobServer.vllm) -> KnobCatalog:
    """Every flag of a model server (vllm, the default, or sglang) as a knob: label, kind,
    default, limits and the server's own description, read from its source at the pinned
    version. Built-in knobs are the ones servtuner captures from a running target."""
    try:
        return service.knob_catalog(server)
    except ValidatorUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error


@router.get("/run-defaults", response_model_exclude_none=True)
def run_defaults(service: Service) -> RunDefaults:
    """The install's Prometheus and price book as RunSpec blocks, for New run to fill in
    (tuning sessions add the same ones to their runs)."""
    return RunDefaults(
        prometheus=service.default_prometheus,
        infra_pricing=service.price_book.infra_pricing(),
        token_pricing=service.price_book.token_pricing(),
        offline=service.offline,
        image_registry=service.image_registry,
    )


@router.get("/workload-presets", response_model_exclude_none=True)
def list_workload_presets(service: Service) -> list[WorkloadPreset]:
    """The workload presets bench carries: named, versioned workloads with suggested SLOs."""
    try:
        return service.workload_presets()
    except ValidatorUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error


@router.get("/search-spaces", response_model_exclude_none=True)
def list_search_spaces(service: Service) -> list[SearchSpaceTemplate]:
    """The starting search spaces bench carries: New session offers the ones that
    fit the managed target, and the space stays editable."""
    try:
        return service.search_space_templates()
    except ValidatorUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error
