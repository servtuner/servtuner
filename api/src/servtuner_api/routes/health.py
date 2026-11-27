"""GET /healthz and GET /version."""

from fastapi import APIRouter

from servtuner_api.resources.runs import HealthResource, VersionResource
from servtuner_api.version import build_info

router = APIRouter()


@router.get("/healthz")
def healthz() -> HealthResource:
    return HealthResource(status="ok")


@router.get("/version")
def version() -> VersionResource:
    """The running build. Signed-in users only, so an unauthenticated scan learns nothing."""
    info = build_info()
    return VersionResource(version=info.version, commit=info.commit)
