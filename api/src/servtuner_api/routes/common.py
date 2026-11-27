"""What the routers share: the service dependency, request-body docs and error mappings."""

from collections.abc import Iterator
from contextlib import contextmanager
from typing import Annotated, Any

from fastapi import Depends, HTTPException, Request, status
from pydantic import BaseModel

from servtuner_api.database import RunRow
from servtuner_api.deletion import DeleteBlockedError
from servtuner_api.deployer.base import DeployInputError, OwnershipError
from servtuner_api.deployer.launcher import LauncherError
from servtuner_api.errors import InvalidDocumentError
from servtuner_api.ids import ULID_PATTERN
from servtuner_api.managed_targets.service import DeployerUnavailableError
from servtuner_api.service import RunService
from servtuner_api.targets.common import (
    CheckUnavailableError,
    SecretStoreUnavailableError,
    TargetConflictError,
    TargetInputError,
    TargetNotFoundError,
)
from servtuner_api.tuning.models import SessionConflictError, SessionNotFoundError
from servtuner_api.tuning.proposer import ProposeError

JSON_CONTENT_TYPE = "application/json"


def json_body_schema(model: type[BaseModel]) -> dict[str, Any]:
    """Document a raw-bytes request body in OpenAPI.

    POST /runs and POST /runs/{id}/events read the raw body, so FastAPI cannot
    infer it. The bytes must reach schema validation untouched: pydantic alone
    accepts the explicit nulls the schema rejects (schema/CLAUDE.md).
    """
    return {
        "requestBody": {
            "required": True,
            "content": {
                JSON_CONTENT_TYPE: {"schema": {"$ref": f"#/components/schemas/{model.__name__}"}}
            },
        }
    }


def service_of(request: Request) -> RunService:
    service: RunService = request.app.state.service
    return service


Service = Annotated[RunService, Depends(service_of)]


def unprocessable(error: InvalidDocumentError) -> HTTPException:
    return HTTPException(
        status.HTTP_422_UNPROCESSABLE_CONTENT,
        detail={"message": "document rejected", "violations": error.violations},
    )


def run_not_found(run_id: str) -> HTTPException:
    return HTTPException(status.HTTP_404_NOT_FOUND, f"run {run_id} not found")


def run_or_404(service: RunService, run_id: str) -> RunRow:
    run = service.get_run(run_id) if ULID_PATTERN.match(run_id) else None
    if run is None:
        raise run_not_found(run_id)
    return run


def blocked(message: str, error: DeleteBlockedError) -> HTTPException:
    return HTTPException(status.HTTP_409_CONFLICT, {"message": message, "blocks": error.blocks})


def experiment_or_404(service: RunService, name: str) -> None:
    if service.experiments.get_row(name) is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"experiment {name} not found")


@contextmanager
def target_errors() -> Iterator[None]:
    """Map the saved-target errors to HTTP. Messages never carry a key."""
    try:
        yield
    except TargetInputError as error:
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            detail={"message": "rejected", "violations": error.violations},
        ) from None
    except TargetNotFoundError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from None
    except TargetConflictError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from None
    except (SecretStoreUnavailableError, CheckUnavailableError) as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from None


@contextmanager
def session_errors() -> Iterator[None]:
    """Map the tuning session errors to HTTP."""
    try:
        yield
    except SessionNotFoundError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from None
    except SessionConflictError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from None
    except ProposeError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from None


@contextmanager
def deploy_errors() -> Iterator[None]:
    """Map the deployer's errors to HTTP."""
    try:
        yield
    except DeployInputError as error:
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            detail={"message": "cannot deploy", "violations": error.problems},
        ) from None
    except OwnershipError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from None
    except (DeployerUnavailableError, LauncherError) as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from None
