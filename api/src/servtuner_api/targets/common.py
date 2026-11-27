"""Saved targets' credential slots, errors, masking, and body parsing shared by the routes."""

import json
from enum import StrEnum
from typing import Any

from pydantic import BaseModel, ValidationError

from servtuner_api.models import validate
from servtuner_api.models.validation import SchemaValidationError

# Runs made from a saved target carry its id under this label, as scheduled runs
# carry servtuner.schedule (the Runs list filters by it).
TARGET_LABEL = "servtuner.target"


CHECK_TARGET_SCHEMA = "check-target-request"


CHECK_TIMEOUT_SECONDS = 120.0  # generate may wait up to 60 s for a cold model


TOKEN_KEY = "token"


USERNAME_KEY = "username"


PASSWORD_KEY = "password"


# The one-time key of a check is written under this Secret name.
ONE_TIME_SECRET = "one-time-key"


# Masked previews are always this wide, so they never tell a key's length.
PREVIEW_WIDTH = 32


PREVIEW_SHOWN = 4


class Slot(StrEnum):
    API_KEY = "api_key"  # target.auth
    ADMIN = "admin"  # target.admin


SLOTS_BY_TYPE: dict[str, tuple[Slot, ...]] = {
    "vllm": (Slot.API_KEY,),
    "litellm": (Slot.API_KEY,),
    "bifrost": (Slot.API_KEY, Slot.ADMIN),
    "openai-compatible": (Slot.API_KEY,),
}


class TargetNotFoundError(LookupError):
    pass


class TargetConflictError(ValueError):
    """Another target already has the name."""


class SecretStoreUnavailableError(RuntimeError):
    """No secret store is configured (SERVTUNER_SECRET_STORE=none), so keys cannot be kept."""


class CheckUnavailableError(RuntimeError):
    """The api has no bench binary, or bench could not run the check."""


class TargetInputError(ValueError):
    """The body is invalid; ``violations`` name each field and problem, never a value."""

    def __init__(self, violations: list[str]) -> None:
        self.violations = violations
        super().__init__("; ".join(violations))


def masked_preview(value: str) -> str:
    """Bifrost's mask, stricter for short keys, always 32 wide.

    24 characters or more: the first and last 4; 13 to 23: the last 4; else none.
    """
    shown = PREVIEW_SHOWN
    if len(value) >= 6 * shown:
        return value[:shown] + "*" * (PREVIEW_WIDTH - 2 * shown) + value[-shown:]
    if len(value) > 3 * shown:
        return "*" * (PREVIEW_WIDTH - shown) + value[-shown:]
    return "*" * PREVIEW_WIDTH


def managed_secret_name(target_id: str, slot: Slot) -> str:
    return f"servtuner-target-{target_id.lower()}-{slot.value.replace('_', '-')}"


def slots_for(target_type: str) -> list[str]:
    return [slot.value for slot in SLOTS_BY_TYPE.get(target_type, (Slot.API_KEY,))]


def parse_body[Model: BaseModel](raw: bytes, model: type[Model]) -> Model:
    """Parse a JSON body into ``model``; errors name fields, never echo input."""
    try:
        document = json.loads(raw)
    except ValueError:
        raise TargetInputError(["the body is not JSON"]) from None
    try:
        return model.model_validate(document)
    except ValidationError as error:
        violations = [
            f"{'.'.join(str(part) for part in issue['loc']) or 'body'}: {issue['msg']}"
            for issue in error.errors(include_input=False, include_url=False)
        ]
        raise TargetInputError(violations) from None


def check_target_shape(target: dict[str, Any], prometheus: dict[str, Any] | None = None) -> None:
    """Hold a target block to the schema a run and a check accept."""
    document: dict[str, Any] = {"target": target}
    if prometheus is not None:
        document["prometheus"] = prometheus
    try:
        validate(CHECK_TARGET_SCHEMA, document)
    except SchemaValidationError as error:
        raise TargetInputError(error.violations) from None


def apply_credentials(target: dict[str, Any], credentials: dict[str, Any]) -> dict[str, Any]:
    """The target block with each set slot's secret reference in place."""
    filled = {key: value for key, value in target.items() if key not in {"auth", "admin"}}
    api_key = credentials.get(Slot.API_KEY)
    if api_key is not None:
        filled["auth"] = {
            "secretRef": {"name": api_key["secret_name"], "key": api_key["keys"]["key"]}
        }
    admin = credentials.get(Slot.ADMIN)
    if admin is not None:
        filled["admin"] = {"secretRef": {"name": admin["secret_name"], **admin["keys"]}}
    return filled
