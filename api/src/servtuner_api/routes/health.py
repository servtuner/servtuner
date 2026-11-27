"""GET /healthz."""

from fastapi import APIRouter

from servtuner_api.resources.runs import HealthResource

router = APIRouter()


@router.get("/healthz")
def healthz() -> HealthResource:
    return HealthResource(status="ok")
