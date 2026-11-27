"""Run resources: a run, its detail, events, files, follow-ups, deletes and defaults."""

import json
from dataclasses import asdict
from datetime import datetime
from typing import TYPE_CHECKING, Any

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import RunEventRow, RunRow
from servtuner_api.models import Event, Summary
from servtuner_api.records.base import RunStatus

if TYPE_CHECKING:  # deletion imports tuning, which imports this module
    from servtuner_api.deletion import RunDeleteImpact


def spec_string(spec_raw: bytes, *path: str) -> str | None:
    """A string field of the stored spec (JSON, as the service stores it), or None."""
    try:
        node: object = json.loads(spec_raw)
    except ValueError:
        return None
    for key in path:
        node = node.get(key) if isinstance(node, dict) else None
    return node if isinstance(node, str) else None


def model_of(spec_raw: bytes) -> str | None:
    """RunSpec target.model."""
    return spec_string(spec_raw, "target", "model")


def workload_preset_of(spec_raw: bytes) -> str | None:
    """RunSpec workload.preset (``name@version``), kept as submitted."""
    return spec_string(spec_raw, "workload", "preset")


class RunResource(BaseModel):
    """Optional fields are absent when unset, like the schemas."""

    model_config = ConfigDict(extra="forbid")

    id: str
    name: str
    experiment: str | None = None
    status: RunStatus
    phase: str | None = None
    target_type: str
    # The model the run tested (RunSpec target.model): the first thing to know about a run.
    model: str | None = None
    # The workload preset the run used (RunSpec workload.preset), when it named one.
    workload_preset: str | None = None
    knobs: dict[str, str | int | float | bool]
    labels: dict[str, str]
    spec_hash: str
    # The store and key Postgres keeps, and where they point under
    # the current configuration.
    bundle_store: str
    bundle_key: str
    bundle_uri: str
    exit_code: int | None = None
    failure_message: str | None = None
    created_at: datetime
    started_at: datetime | None = None
    finished_at: datetime | None = None
    # The run's SLO-max RPS from run_metrics, when it has one and is not
    # excluded from comparison: the number a person looks for first in a list of runs.
    slo_max_rps: float | None = None
    # Who created the run: absent for runs from before user auth, from auth.mode none, or
    # started by servtuner itself (a schedule or a tuning session).
    created_by: str | None = None
    created_by_username: str | None = None

    @classmethod
    def from_row(
        cls,
        row: RunRow,
        bundle_uri: str,
        slo_max_rps: float | None = None,
        created_by_username: str | None = None,
    ) -> RunResource:
        return cls(
            id=row.id,
            name=row.name,
            experiment=row.experiment,
            status=RunStatus(row.status),
            phase=row.phase,
            target_type=row.target_type,
            model=model_of(row.spec_raw),
            workload_preset=workload_preset_of(row.spec_raw),
            knobs=row.knobs,
            labels=row.labels,
            spec_hash=row.spec_hash,
            bundle_store=row.bundle_store,
            bundle_key=row.bundle_key,
            bundle_uri=bundle_uri,
            exit_code=row.exit_code,
            failure_message=row.failure_message,
            created_at=row.created_at,
            started_at=row.started_at,
            finished_at=row.finished_at,
            slo_max_rps=slo_max_rps,
            created_by=row.created_by,
            created_by_username=created_by_username,
        )


class RunDetail(RunResource):
    """A run's metadata plus the summary from its bundle, once bench has written one."""

    summary: Summary | None = None


class DeleteImpactResource(BaseModel):
    """What deleting a run would do: nothing is deleted while blocks is non-empty."""

    run_id: str
    blocks: list[str]
    analyses: list[str]
    bundle_files: int
    notes: list[str]

    @classmethod
    def of(cls, impact: RunDeleteImpact) -> DeleteImpactResource:
        return cls(**asdict(impact))


class HealthResource(BaseModel):
    status: str


def event_from_row(row: RunEventRow) -> Event:
    return Event.model_validate(
        {
            "run_id": row.run_id,
            "sequence": row.sequence,
            "time": row.time.isoformat().replace("+00:00", "Z"),
            "step": row.step,
            "state": row.state,
            "phase": row.phase,
            "message": row.message,
        }
    )


class BundleFileResource(BaseModel):
    """One file of a run's bundle, as GET /runs/{id}/files lists it."""

    model_config = ConfigDict(extra="forbid")

    path: str
    size_bytes: int


class FollowupCreate(BaseModel):
    """POST /runs/{id}/followup: test one of the run's knob recommendations."""

    model_config = ConfigDict(extra="forbid")

    recommendation_id: str
    repeats: int = Field(default=3, ge=1, le=10)
    experiment: str | None = Field(
        default=None,
        min_length=1,
        max_length=200,
        description="The runs' experiment. Absent: the source run's, else <run name>-<knob>.",
    )


class FollowupResource(BaseModel):
    """Follow-up runs waiting for the change they test (status awaiting_config).

    Apply ``patch`` (or set ``knob`` to ``value`` by hand), then confirm each
    run with POST /runs/{id}/confirm-applied to launch it.
    """

    model_config = ConfigDict(extra="forbid")

    runs: list[RunResource]
    knob: str
    value: str | int | float | bool
    patch: str | None = None


class SpecValidationResource(BaseModel):
    """POST /specs/validate: servtuner validate's verdict on a RunSpec (YAML or JSON)."""

    model_config = ConfigDict(extra="forbid")

    valid: bool
    name: str | None = None
    errors: list[str]
    warnings: list[str]


class RunDefaults(BaseModel):
    """GET /run-defaults: what this install fills into a new RunSpec, so New run names it."""

    model_config = ConfigDict(extra="forbid")

    # The install's Prometheus as a RunSpec prometheus block (the one tuning sessions' runs use);
    # absent without one. Without it a run collects no server-side series.
    prometheus: dict[str, Any] | None = None
    # The price book as a RunSpec analysis.infra_pricing block; absent while it prices
    # nothing. A run on a host target is priced from its host instead.
    infra_pricing: dict[str, Any] | None = None
    # The price book's datasheet as a RunSpec analysis.token_pricing block, for a run
    # priced by the token; absent without one.
    token_pricing: dict[str, Any] | None = None
    # Air-gapped installs: no internet access, and the registry every image comes
    # from (New managed target prefixes its vLLM image with it).
    offline: bool = False
    image_registry: str | None = None
