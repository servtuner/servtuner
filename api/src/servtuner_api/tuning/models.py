"""A tuning session's request and resource, its budget, statuses and errors."""

from __future__ import annotations

from collections.abc import Mapping
from datetime import datetime
from typing import Any, Literal

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import TuningSessionRow
from servtuner_api.managed_targets.spec import DNS_LABEL
from servtuner_api.tuning.proposer import PRESET_REFERENCE

SESSION_LABEL = "servtuner.dev/tuning-session"


Status = Literal["running", "awaiting_approval", "done", "failed", "cancelled"]


class SessionBudget(BaseModel):
    """When the session stops even if the search has not converged."""

    model_config = ConfigDict(extra="forbid")

    max_runs: int = Field(ge=1, le=500)
    max_hours: float = Field(gt=0, le=72)


# Sessions one new session may reuse: enough for a few earlier attempts, few enough to read.
MAX_REUSED_SESSIONS = 10


FEASIBILITY_TIER = "feasibility"


class SessionCreate(BaseModel):
    """POST /tuning-sessions."""

    model_config = ConfigDict(extra="forbid")

    managed_target_id: str
    preset: str = Field(pattern=PRESET_REFERENCE)
    streaming: bool = True
    slos: dict[str, str | float]
    space: dict[str, Any]
    budget: SessionBudget
    mode: Literal["auto", "manual"] = "auto"
    stage_duration: str = "90s"
    warmup: str = "30s"
    experiment: str | None = Field(default=None, pattern=DNS_LABEL)
    # Earlier sessions whose boot results and runs seed this one: same managed target,
    # preset and SLOs, the target not edited since they started, and on a host the same vLLM.
    reuse_from: list[str] = Field(default_factory=list, max_length=MAX_REUSED_SESSIONS)


class SessionResource(BaseModel):
    model_config = ConfigDict(extra="forbid")

    id: str
    managed_target_id: str
    status: Status
    experiment: str
    request: SessionCreate
    step: dict[str, Any] | None = None
    proposal: dict[str, Any] | None = None
    boot_results: list[dict[str, Any]]
    history: list[dict[str, Any]]
    outcome: dict[str, Any] | None = None
    runs_started: int
    created_at: datetime
    updated_at: datetime
    finished_at: datetime | None = None

    @classmethod
    def from_row(cls, row: TuningSessionRow) -> SessionResource:
        return cls(
            id=row.id,
            managed_target_id=row.managed_target_id,
            status=row.status,  # type: ignore[arg-type]
            experiment=row.experiment,
            request=SessionCreate.model_validate(row.request),
            step=row.step,
            proposal=row.proposal,
            boot_results=row.boot_results,
            history=row.history,
            outcome=row.outcome,
            runs_started=row.runs_started,
            created_at=row.created_at,
            updated_at=row.updated_at,
            finished_at=row.finished_at,
        )


# servtuner propose's objective when a space names none (search-space.schema.json).
BENCH_DEFAULT_OBJECTIVE = "slo_rps_per_gpu"


def checked_space(space: Mapping[str, Any], objectives: list[str]) -> dict[str, Any]:
    """The space with an objective the target's runs can compute.

    A space with none gets the target's default (a host's is slo_max_rps: servtuner propose's
    slo_rps_per_gpu needs a GPU count only a host with declared hardware gives). One the runs
    cannot compute is refused now, not after the first run.
    """
    objective = space.get("objective")
    if objective is None:
        # servtuner propose's own default needs no writing; a host's differs from it.
        default = objectives[0]
        if default == BENCH_DEFAULT_OBJECTIVE:
            return dict(space)
        return {**space, "objective": default}
    if objective not in objectives:
        raise SessionConflictError(
            f"objective {objective} cannot be computed for this target's runs: it can rank by "
            f"{', '.join(objectives)} (a host needs its hardware for per-GPU objectives and a "
            "price for cost ones)"
        )
    return dict(space)


class SessionConflictError(ValueError):
    """Another session holds the lease, or the session is not in a state that allows this."""


class SessionNotFoundError(LookupError):
    pass


# The most runs a session's history fetch considers.
MAX_SESSION_RUNS = 1000
