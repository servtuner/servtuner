"""Saved targets: keys are write-only; no model ever holds a value."""

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, SecretStr

from servtuner_api.database import TargetRow
from servtuner_api.models import CheckTargetResult
from servtuner_api.models.generated.runspec_schema import Prometheus, Target
from servtuner_api.targets.pricing import RunPricing, TargetPricing

MAX_TARGET_NAME = 63  # a DNS label, so the name can appear in Secret names and labels


MAX_TARGET_DESCRIPTION = 500


TARGET_NAME_PATTERN = r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"


class TargetCreate(BaseModel):
    """POST /targets and PUT /targets/{id}: a RunSpec target block under a name.

    ``target`` must not carry ``auth`` or ``admin``: keys are set with
    PUT /targets/{id}/credentials/{slot}, so this body never holds one.
    """

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=MAX_TARGET_NAME, pattern=TARGET_NAME_PATTERN)
    description: str | None = Field(default=None, max_length=MAX_TARGET_DESCRIPTION)
    target: Target
    # How runs against it are priced; absent: unpriced.
    pricing: TargetPricing | None = None


class ExternalSecret(BaseModel):
    """A Secret the user manages: its name and the keys that hold the credential."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=253)
    key: str | None = Field(default=None, description="api_key: the key holding the API key")
    token_key: str | None = Field(default=None, description="admin: the key holding a token")
    username_key: str | None = Field(default=None, description="admin: basic auth username key")
    password_key: str | None = Field(default=None, description="admin: basic auth password key")


class CredentialInput(BaseModel):
    """PUT /targets/{id}/credentials/{slot}: write-only. Exactly one form:

    - ``value``: a key or token, which servtuner stores in a Secret it manages;
    - ``username`` and ``password``: basic auth (the ``admin`` slot only);
    - ``external``: a Secret you manage, by name.
    """

    model_config = ConfigDict(extra="forbid")

    value: SecretStr | None = Field(default=None, min_length=1)
    username: str | None = Field(default=None, min_length=1)
    password: SecretStr | None = Field(default=None, min_length=1)
    external: ExternalSecret | None = None


class CredentialStatus(BaseModel):
    """Where a credential lives and how it looks, never its value."""

    model_config = ConfigDict(extra="forbid")

    is_set: bool
    source: Literal["managed", "external"] | None = None
    form: Literal["token", "basic"] | None = None
    preview: str | None = Field(default=None, description="masked: never the value")
    username: str | None = Field(default=None, description="basic auth: not a secret")
    secret_name: str | None = None
    set_at: datetime | None = None


class TargetCheckView(BaseModel):
    """A check-target result and where it ran from."""

    model_config = ConfigDict(extra="forbid")

    result: CheckTargetResult
    checked_from: str


class TargetResource(BaseModel):
    """A saved target. ``target`` is what New run copies into a RunSpec."""

    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    description: str | None = None
    type: str
    target: Target
    pricing: TargetPricing | None = None
    # What New run copies: the target with its declared hardware (a host or node type), and the
    # RunSpec analysis blocks of its prices; pricing_problems says what cannot price it now.
    run_target: Target
    run_analysis: dict[str, object] | None = None
    pricing_problems: list[str] = Field(default_factory=list)
    slots: list[str]
    credentials: dict[str, CredentialStatus]
    last_check: TargetCheckView | None = None
    created_at: datetime
    updated_at: datetime

    @classmethod
    def from_row(
        cls, row: TargetRow, slots: list[str], run_pricing: RunPricing | None = None
    ) -> TargetResource:
        credentials = {slot: CredentialStatus(is_set=False) for slot in slots}
        for slot, stored in row.credentials.items():
            credentials[slot] = CredentialStatus(
                is_set=True,
                source=stored["source"],
                form=stored.get("form"),
                preview=stored.get("preview"),
                username=stored.get("username"),
                secret_name=stored["secret_name"] if stored["source"] == "external" else None,
                set_at=stored["set_at"],
            )
        return cls(
            id=row.id,
            name=row.name,
            description=row.description,
            type=row.target_type,
            target=Target.model_validate(row.target),
            pricing=None if row.pricing is None else TargetPricing.model_validate(row.pricing),
            run_target=Target.model_validate(
                row.target
                if run_pricing is None or run_pricing.hardware is None
                else {**row.target, "hardware": run_pricing.hardware}
            ),
            run_analysis=(run_pricing.analysis or None) if run_pricing is not None else None,
            pricing_problems=[] if run_pricing is None else run_pricing.problems,
            slots=slots,
            credentials=credentials,
            last_check=TargetCheckView.model_validate(row.last_check) if row.last_check else None,
            created_at=row.created_at,
            updated_at=row.updated_at,
        )


class SavedTargetCheck(BaseModel):
    """POST /targets/{id}/check: optionally a one-time key, used for this check only."""

    model_config = ConfigDict(extra="forbid")

    generate: bool = False
    api_key: SecretStr | None = Field(default=None, min_length=1)


class UnsavedTargetCheck(BaseModel):
    """POST /targets/check: an unsaved target block, as in New run's YAML."""

    model_config = ConfigDict(extra="forbid")

    target: Target
    prometheus: Prometheus | None = None
    generate: bool = False
