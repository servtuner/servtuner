"""The FastAPI app. FastAPI's OpenAPI document is the contract with the frontend.

Routes live in ``routes/`` (one router per domain) and ``auth/``; every one passes the auth
guard and has an entry in ``auth/access.py``.
"""

from collections.abc import AsyncGenerator, Callable
from contextlib import asynccontextmanager

from fastapi import Depends, FastAPI

from servtuner_api.auth import routes as auth_routes
from servtuner_api.auth import users as user_routes
from servtuner_api.auth.guard import authorize_request
from servtuner_api.auth.service import AuthService
from servtuner_api.hosts import HostCreate
from servtuner_api.managed_targets.views import BootProbeRequest, DeployRequest, ManagedTargetCreate
from servtuner_api.models import Event, RunSpec
from servtuner_api.openapi_components import publish_request_models
from servtuner_api.price_book import PriceBook
from servtuner_api.resources.planning import PlanCreate, ScenarioCreate
from servtuner_api.resources.targets import (
    CredentialInput,
    SavedTargetCheck,
    TargetCreate,
    UnsavedTargetCheck,
)
from servtuner_api.routes import (
    experiments,
    health,
    hosts,
    managed_targets,
    planning,
    pricing,
    runs,
    schedules,
    targets,
    tuning,
)
from servtuner_api.service import RunService
from servtuner_api.tuning.models import SessionCreate

# Order matters only where paths could overlap; this is the order the routes were written in.
ROUTERS = (
    health.router,
    runs.router,
    planning.router,
    experiments.router,
    schedules.router,
    targets.router,
    managed_targets.router,
    hosts.router,
    pricing.router,
    tuning.router,
    auth_routes.router,
    user_routes.router,
)
# Raw-body routes mention no model in their signature, so these are published explicitly.
REQUEST_MODELS = (
    RunSpec,
    Event,
    PlanCreate,
    ScenarioCreate,
    TargetCreate,
    CredentialInput,
    SavedTargetCheck,
    UnsavedTargetCheck,
    ManagedTargetCreate,
    HostCreate,
    PriceBook,
    DeployRequest,
    BootProbeRequest,
    SessionCreate,
)


def create_app(
    service_factory: Callable[[], RunService],
    auth_factory: Callable[[], AuthService] = AuthService.unchecked,
) -> FastAPI:
    """Build the app; ``service_factory`` runs at startup and its service stops at shutdown.

     ``auth_factory`` builds the auth service whose guard every route passes through
    ; the default checks nothing, which is auth.mode none.
    """

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncGenerator[None]:
        app.state.auth = auth_factory()
        app.state.auth.start()
        app.state.service = service_factory()
        app.state.service.start()
        yield
        app.state.service.stop()

    app = FastAPI(
        title="servtuner API",
        version="0.0.0",
        lifespan=lifespan,
        dependencies=[Depends(authorize_request)],
    )
    for router in ROUTERS:
        app.include_router(router)
    publish_request_models(app, list(REQUEST_MODELS))
    return app
