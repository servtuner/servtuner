"""Reusing earlier sessions' evidence: what carries over, and what forbids it."""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from servtuner_api.database import ManagedTargetRow, RunRow, TuningSessionRow
from servtuner_api.managed_targets.spec import HostTargetSpec, parse_spec
from servtuner_api.records.base import RunFilters, RunStatus
from servtuner_api.records.repository import RunRepository
from servtuner_api.tuning.models import (
    FEASIBILITY_TIER,
    MAX_SESSION_RUNS,
    SESSION_LABEL,
    SessionConflictError,
    SessionCreate,
)
from servtuner_api.tuning.ports import ManagedOps


def _vllm_version_problem(prior: TuningSessionRow, current: str) -> str | None:
    """Why a host session's evidence no longer holds: its boots ran another vLLM, or none says."""
    measured = {boot["vllm_version"] for boot in prior.boot_results if boot.get("vllm_version")}
    if not measured:
        return "its boots recorded no vLLM version (it predates version recording)"
    if measured != {current}:
        return f"it ran vLLM {', '.join(sorted(measured))}; the host now has {current}"
    return None


def session_bundles(
    repository: RunRepository, bundle_key: Callable[[RunRow], str], session_id: str
) -> list[str]:
    """A session's succeeded runs' bundle keys, oldest first."""
    runs = repository.list_runs(
        RunFilters(label=(SESSION_LABEL, session_id.lower()), status=RunStatus.SUCCEEDED),
        MAX_SESSION_RUNS,
    )
    return [bundle_key(run) for run in reversed(runs)]


def reused_evidence(
    body: SessionCreate,
    target: ManagedTargetRow,
    repository: RunRepository,
    managed: ManagedOps,
    bundle_key: Callable[[RunRow], str],
) -> tuple[list[dict[str, Any]], list[str]]:
    """The boot results and run bundles of the sessions body reuses, after checking
    that their evidence still describes this target: same target, preset and SLOs, the target
    unedited since, and for a host the user's word that its vLLM is unchanged."""
    if not body.reuse_from:
        return [], []
    # A host's vLLM can change without its target changing (the launcher runs what is
    # installed), so its evidence holds only for the version it was measured on.
    host_version = (
        host_vllm_version(managed, target.id)
        if isinstance(parse_spec(target.spec), HostTargetSpec)
        else None
    )
    feasibility = {
        knob["name"] for knob in body.space.get("knobs", []) if knob.get("tier") == FEASIBILITY_TIER
    }
    boots: list[dict[str, Any]] = []
    bundles: list[str] = []
    for session_id in dict.fromkeys(body.reuse_from):
        prior = repository.get_tuning_session(session_id)
        if prior is None:
            raise SessionConflictError(f"no tuning session {session_id} to reuse")
        problem = reuse_problem(prior, body, target) or (
            _vllm_version_problem(prior, host_version) if host_version is not None else None
        )
        if problem:
            raise SessionConflictError(f"cannot reuse session {session_id}: {problem}")
        # An infeasible boot rules out whatever contains its knobs, so it always carries
        # over; servtuner propose needs a feasible one to name every feasibility knob.
        boots += [
            boot
            for boot in prior.boot_results
            if boot.get("outcome") == "infeasible" or feasibility <= set(boot.get("knobs", {}))
        ]
        bundles += session_bundles(repository, bundle_key, session_id)
    return boots, bundles


def host_vllm_version(managed: ManagedOps, target_id: str) -> str:
    version = managed.installed_vllm_version(target_id)
    if version is None:
        raise SessionConflictError(
            "the host's launcher did not report its vLLM version (unreachable, or a bench "
            "launcher older than GET /server/version), so earlier sessions cannot be reused"
        )
    return version


def reuse_problem(
    prior: TuningSessionRow, body: SessionCreate, target: ManagedTargetRow
) -> str | None:
    prior_request = SessionCreate.model_validate(prior.request)
    if prior.status in ("running", "awaiting_approval"):
        return "it is still active"
    if prior.managed_target_id != body.managed_target_id:
        return "it tuned another managed target"
    if prior_request.preset != body.preset:
        return f"it used preset {prior_request.preset}, not {body.preset}"
    if prior_request.slos != body.slos:
        return "its SLOs differ"
    if target.updated_at > prior.created_at:
        return "the target was edited since it started (its image or flags may differ)"
    return None
