"""The request and response models of the managed-target routes."""

from __future__ import annotations

from datetime import datetime
from typing import TYPE_CHECKING, Any, Literal

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import HostRow, ManagedTargetRow
from servtuner_api.hosts import declared_hardware, infra_pricing
from servtuner_api.managed_targets.runs import (
    credential_statuses,
    run_labels,
    run_target,
    session_objectives,
)
from servtuner_api.managed_targets.spec import (
    DNS_LABEL,
    MAX_DESCRIPTION,
    AnyTargetSpec,
    HostTargetSpec,
    parse_spec,
)
from servtuner_api.models.generated.boot_results_schema import TuningBootResult
from servtuner_api.models.generated.runspec_schema import Target
from servtuner_api.resources.targets import CredentialStatus, TargetCheckView

if TYPE_CHECKING:  # the deployer imports this package
    from servtuner_api.deployer.base import Readiness


class ManagedTargetCreate(BaseModel):
    """POST /managed-targets and PUT /managed-targets/{id}."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(pattern=DNS_LABEL)
    description: str | None = Field(default=None, max_length=MAX_DESCRIPTION)
    # Kubernetes: where the vLLM is deployed (a tuning namespace). Host: the namespace label its
    # series carry in Prometheus.
    namespace: str = Field(pattern=DNS_LABEL)
    spec: AnyTargetSpec


class DeployRequest(BaseModel):
    """POST /managed-targets/{id}/deploy: the vLLM knobs of this configuration."""

    model_config = ConfigDict(extra="forbid")

    knobs: dict[str, str | int | float | bool] = Field(default_factory=dict)


class DeploymentView(BaseModel):
    """The last deploy: the knobs asked for and the container args they rendered."""

    model_config = ConfigDict(extra="forbid")

    knobs: dict[str, str | int | float | bool]
    args: list[str]
    deployed_at: datetime
    # Set by a scale-down (a user's, or a session's at its end); a deploy starts it over.
    scaled_down_at: datetime | None = None


class BootFailureView(BaseModel):
    model_config = ConfigDict(extra="forbid")

    kind: Literal["infeasible", "render_bug", "deploy_defect", "sharing", "unknown"]
    rule: str
    message: str
    excerpt: list[str]
    exit_code: int | None = None


class ReadinessView(BaseModel):
    """GET /managed-targets/{id}/readiness: one poll."""

    model_config = ConfigDict(extra="forbid")

    state: Literal["pending", "ready", "infeasible", "failed"]
    reason: str
    failure: BootFailureView | None = None
    check: TargetCheckView | None = None

    @classmethod
    def of(cls, readiness: Readiness) -> ReadinessView:
        failure = readiness.failure
        return cls(
            state=readiness.state,
            reason=readiness.reason,
            failure=(
                None
                if failure is None
                else BootFailureView(
                    kind=failure.kind,
                    rule=failure.rule,
                    message=failure.message,
                    excerpt=failure.excerpt,
                    exit_code=failure.exit_code,
                )
            ),
            check=readiness.check,
        )


class BootProbeRequest(BaseModel):
    """POST /managed-targets/{id}/boot-probe: the workload's mean prompt + generated tokens."""

    model_config = ConfigDict(extra="forbid")

    tokens_per_request: float = Field(gt=0)


class BootProbeView(BaseModel):
    """A boot probe poll: the readiness, and the tuningBootResult once the boot has ended."""

    model_config = ConfigDict(extra="forbid")

    readiness: ReadinessView
    result: TuningBootResult | None = None


class LauncherCheckView(BaseModel):
    """POST /managed-targets/{id}/check-launcher: the host's launcher, as the api sees it."""

    model_config = ConfigDict(extra="forbid")

    status: Literal["ok", "unreachable", "refused", "no_token", "error"]
    message: str
    vllm_state: str | None = None
    vllm_args: list[str] | None = None


class LogView(BaseModel):
    """GET /managed-targets/{id}/logs: the end of vLLM's log, for a person to read."""

    model_config = ConfigDict(extra="forbid")

    source: str
    lines: list[str]


class ManagedTargetResource(BaseModel):
    """A managed target, with the RunSpec target block its runs get."""

    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    description: str | None = None
    namespace: str
    spec: AnyTargetSpec
    run_target: Target
    # The RunSpec analysis block its runs get: a host target's host's prices, or the
    # install's price book for a Kubernetes target; None when unpriced.
    run_analysis: dict[str, Any] | None = None
    # What a tuning session on it may rank by, the default first.
    session_objectives: list[str] = Field(default_factory=list)
    # The labels a run of this target carries (New run copies them; GET /runs?label= finds them).
    run_labels: dict[str, str]
    # A host target's launcher token and vLLM key: set or not, and where; never a value.
    credentials: dict[str, CredentialStatus] = Field(default_factory=dict)
    deployment: DeploymentView | None = None
    created_at: datetime
    updated_at: datetime

    @classmethod
    def from_row(
        cls,
        row: ManagedTargetRow,
        host: HostRow | None = None,
        install_pricing: dict[str, object] | None = None,
    ) -> ManagedTargetResource:
        """The resource; ``host`` is the host a host target names, for its runs' hardware and
        prices, and ``install_pricing`` the price book's block, for a Kubernetes target's runs
        (ManagedTargetService.resource looks both up)."""
        spec = parse_spec(row.spec)
        if isinstance(spec, HostTargetSpec):
            pricing = None if host is None else infra_pricing(host)
        else:
            pricing = install_pricing
        hardware = None if host is None else declared_hardware(host)
        return cls(
            id=row.id,
            name=row.name,
            description=row.description,
            namespace=row.namespace,
            spec=spec,
            run_target=Target.model_validate(
                run_target(
                    row.name,
                    row.namespace,
                    spec,
                    hardware,
                )
            ),
            run_analysis=None if pricing is None else {"infra_pricing": pricing},
            session_objectives=session_objectives(spec, hardware, pricing is not None),
            run_labels=run_labels(row.id),
            credentials=credential_statuses(row.id, spec),
            deployment=(
                None if row.deployment is None else DeploymentView.model_validate(row.deployment)
            ),
            created_at=row.created_at,
            updated_at=row.updated_at,
        )
