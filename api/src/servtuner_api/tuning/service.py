"""The tuning state machine: one transition per tick, stored, so a restarted api resumes."""

from __future__ import annotations

import json
import logging
from collections.abc import Callable, Mapping
from datetime import UTC, datetime
from typing import Any

from servtuner_api.database import TuningSessionRow
from servtuner_api.ids import new_ulid
from servtuner_api.records.base import (
    FINAL_STATUSES,
    REWATCH_LIMIT,
    UNFINISHED_LAUNCHED,
    RunFilters,
    RunStatus,
)
from servtuner_api.records.repository import RunRepository
from servtuner_api.tuning.evidence import reused_evidence, session_bundles
from servtuner_api.tuning.models import (
    SESSION_LABEL,
    SessionConflictError,
    SessionCreate,
    SessionNotFoundError,
    Status,
    checked_space,
)
from servtuner_api.tuning.outcome import with_pareto, with_recommendation
from servtuner_api.tuning.ports import ManagedOps, RunOps
from servtuner_api.tuning.proposer import ProposeError, Proposer
from servtuner_api.tuning.run_specs import build_run_spec, tokens_per_request

logger = logging.getLogger(__name__)


# A deploy that is neither ready nor failed after this long is stuck (an
# unschedulable pod, a hung download): 45 min covers a 30 min model load.
DEPLOY_TIMEOUT_SECONDS = 45 * 60


# Consecutive tick errors (a cluster or database hiccup) before the session gives up.
MAX_TICK_ERRORS = 5


class TuningService:
    """Creates sessions and advances them one transition per tick."""

    def __init__(
        self,
        repository: RunRepository,
        runs: RunOps,
        managed: ManagedOps,
        proposer: Proposer,
        presets: Callable[[], list[Any]],
        prometheus: Mapping[str, Any] | None = None,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        self._repository = repository
        self._runs = runs
        self._managed = managed
        self._proposer = proposer
        self._presets = presets
        self._prometheus = prometheus
        self._clock = clock

    # --- the api

    def create(self, body: SessionCreate) -> TuningSessionRow:
        target = self._managed.get(body.managed_target_id)
        objectives = self._managed.resource(target).session_objectives
        body = body.model_copy(update={"space": checked_space(body.space, objectives)})
        self._preset(body.preset)  # an unknown preset fails now, not at the first run
        reused_boots, reused_bundles = reused_evidence(
            body, target, self._repository, self._managed, self._runs.bundle_key
        )
        # A space servtuner propose refuses fails now too, with the reused evidence: the first
        # proposal is a dry run.
        try:
            self._proposer.propose(body.space, reused_bundles, reused_boots)
        except ProposeError as error:
            if error.is_invalid_input:
                raise SessionConflictError(f"the search space is invalid: {error}") from None
            raise
        now = self._clock()
        session_id = new_ulid()
        row = TuningSessionRow(
            id=session_id,
            managed_target_id=target.id,
            status="running",
            request=body.model_dump(mode="json"),
            experiment=body.experiment or f"tune-{target.name}-{session_id[-6:].lower()}",
            step={"phase": "propose"},
            proposal=None,
            boot_results=reused_boots,
            history=[],
            outcome=None,
            runs_started=0,
            created_at=now,
            updated_at=now,
            finished_at=None,
        )
        if not self._repository.insert_tuning_session(row):
            raise SessionConflictError(
                "another tuning session is active: one at a time (they share the GPU)"
            )
        return row

    def get(self, session_id: str) -> TuningSessionRow:
        row = self._repository.get_tuning_session(session_id)
        if row is None:
            raise SessionNotFoundError(f"no tuning session {session_id}")
        return row

    def list_sessions(self) -> list[TuningSessionRow]:
        return self._repository.list_tuning_sessions()

    def refuse_if_tuning(self, managed_target_id: str) -> None:
        """A user's deploy, scale-down, edit or delete would pull the target out from under an
        active session (the session's own steps call the managed targets directly)."""
        for row in self._repository.list_tuning_sessions(active_only=True):
            if row.managed_target_id == managed_target_id:
                raise SessionConflictError(
                    f"tuning session {row.id} is using this target: cancel it first"
                )

    def approve(self, session_id: str) -> TuningSessionRow:
        """Manual mode: run the pending proposal."""
        row = self.get(session_id)
        if row.status != "awaiting_approval" or row.step is None:
            raise SessionConflictError(
                f"session {session_id} is {row.status}, not awaiting approval"
            )
        return self._save(row, status="running", step={**row.step, "phase": "deploy"})

    def cancel(self, session_id: str) -> TuningSessionRow:
        """Stop issuing steps, cancel the session's unfinished runs, and free the GPU.

        The runs go first: once vLLM is stopped they could only fail against it.
        """
        row = self.get(session_id)
        if row.status not in ("running", "awaiting_approval"):
            raise SessionConflictError(f"session {session_id} is already {row.status}")
        for status in UNFINISHED_LAUNCHED:
            for run in self._runs.list_runs(
                RunFilters(label=(SESSION_LABEL, row.id.lower()), status=status), REWATCH_LIMIT
            ):
                self._runs.cancel_run(run.id)
        return self._finish(row, "cancelled", {"reason": "cancelled"})

    # --- the loop

    def tick_all(self) -> None:
        for row in self._repository.list_tuning_sessions(active_only=True):
            if row.status == "running":
                self.tick(row.id)

    def tick(self, session_id: str) -> TuningSessionRow:
        row = self.get(session_id)
        if row.status != "running":
            return row
        step = dict(row.step or {"phase": "propose"})
        try:
            handler = {
                "propose": self._propose,
                "deploy": self._deploy,
                "booting": self._booting,
                "running": self._running,
            }[step["phase"]]
            return handler(row, step)
        except ProposeError as error:
            return self._finish(row, "failed", {"reason": f"servtuner propose: {error}"})
        except Exception as error:  # a transient cluster or database error: retry, then give up
            logger.exception("tuning session %s: tick failed", session_id)
            errors = int(step.get("errors", 0)) + 1
            if errors >= MAX_TICK_ERRORS:
                return self._finish(
                    row, "failed", {"reason": f"{errors} ticks in a row failed: {error}"}
                )
            return self._save(row, step={**step, "errors": errors})

    def _propose(self, row: TuningSessionRow, step: dict[str, Any]) -> TuningSessionRow:
        request = SessionCreate.model_validate(row.request)
        proposal = self._proposer.propose(request.space, self._bundle_keys(row), row.boot_results)
        if proposal["status"] == "done":
            return self._finish(
                row, "done", {**proposal["done"], "proposal_phase": proposal.get("phase")}, proposal
            )
        proposed = proposal["proposals"][0]
        if proposed["action"] == "run":
            repeats = int(proposed.get("repeats", 1))
            if row.runs_started + repeats > request.budget.max_runs:
                return self._out_of_budget(row, proposal, f"{request.budget.max_runs} runs")
        if self._elapsed_hours(row) > request.budget.max_hours:
            return self._out_of_budget(row, proposal, f"{request.budget.max_hours} h")
        status: Status = "awaiting_approval" if request.mode == "manual" else "running"
        phase = "approval" if request.mode == "manual" else "deploy"
        return self._save(
            row, status=status, proposal=proposal, step={"phase": phase, "proposed": proposed}
        )

    def _deploy(self, row: TuningSessionRow, step: dict[str, Any]) -> TuningSessionRow:
        knobs = step["proposed"]["knobs"]
        self._managed.deploy(row.managed_target_id, json.dumps({"knobs": knobs}).encode())
        started = self._clock().isoformat()
        return self._save(
            row, step={"phase": "booting", "proposed": step["proposed"], "deployed_at": started}
        )

    def _booting(self, row: TuningSessionRow, step: dict[str, Any]) -> TuningSessionRow:
        proposed = step["proposed"]
        request = SessionCreate.model_validate(row.request)
        if proposed["action"] == "boot_probe":
            tokens = tokens_per_request(self._preset(request.preset))
            view = self._managed.boot_probe(
                row.managed_target_id, json.dumps({"tokens_per_request": tokens}).encode()
            )
            readiness = view.readiness
            if view.result is not None:
                result = view.result.model_dump(mode="json", exclude_none=True)
                return self._step_done(row, step, boot_result=result)
        else:
            readiness = self._managed.readiness(row.managed_target_id)
            if readiness.state == "ready":
                return self._save(
                    row,
                    step={**step, "phase": "running", "repeat": 0, "run_ids": [], "retried": False},
                )
            if readiness.state == "infeasible":
                result = {
                    "knobs": proposed["knobs"],
                    "outcome": "infeasible",
                    "reason": readiness.reason,
                }
                return self._step_done(row, step, boot_result=result)
        if readiness.state == "failed":
            return self._finish(row, "failed", {"reason": f"deploy failed: {readiness.reason}"})
        deployed_at = datetime.fromisoformat(step["deployed_at"])
        if (self._clock() - deployed_at).total_seconds() > DEPLOY_TIMEOUT_SECONDS:
            minutes = DEPLOY_TIMEOUT_SECONDS // 60
            reason = f"not ready after {minutes} min: {readiness.reason}"
            return self._finish(row, "failed", {"reason": reason})
        return self._save(row, step={**step, "waiting": readiness.reason})

    def _running(self, row: TuningSessionRow, step: dict[str, Any]) -> TuningSessionRow:
        proposed = step["proposed"]
        repeats = int(proposed.get("repeats", 1))
        current = step.get("current")
        if current is not None:
            run = self._runs.get_run(current)
            if run is None or RunStatus(run.status) not in FINAL_STATUSES:
                return row
            run_ids = [*step["run_ids"], current]
            if RunStatus(run.status) != RunStatus.SUCCEEDED and not step["retried"]:
                # One retry: a run that dies mid-ladder leaves the confirmation without a winner.
                return self._save(
                    row, step={**step, "current": None, "run_ids": run_ids, "retried": True}
                )
            repeat = int(step["repeat"]) + 1
            if repeat >= repeats:
                return self._step_done(row, {**step, "run_ids": run_ids})
            return self._save(
                row,
                step={
                    **step,
                    "current": None,
                    "run_ids": run_ids,
                    "repeat": repeat,
                    "retried": False,
                },
            )
        request = SessionCreate.model_validate(row.request)
        target = self._managed.get(row.managed_target_id)
        resource = self._managed.resource(target)
        run_target = resource.run_target.model_dump(mode="json", exclude_none=True)
        spec = build_run_spec(
            row.id,
            request,
            row.experiment,
            run_target,
            proposed,
            int(step["repeat"]),
            self._prometheus,
            resource.run_analysis,
        )
        created = self._runs.create_run(json.dumps(spec).encode())
        return self._save(
            row, step={**step, "current": created.id}, runs_started=row.runs_started + 1
        )

    # --- helpers

    def _step_done(
        self, row: TuningSessionRow, step: dict[str, Any], boot_result: dict[str, Any] | None = None
    ) -> TuningSessionRow:
        proposed = step["proposed"]
        entry: dict[str, Any] = {
            "action": proposed["action"],
            "config_id": proposed["config_id"],
            "knobs": proposed["knobs"],
            "finished_at": self._clock().isoformat(),
        }
        if "rationale" in proposed:
            entry["rationale"] = proposed["rationale"]
        boot_results = row.boot_results
        if boot_result is not None:
            entry["boot_result"] = boot_result
            boot_results = [*row.boot_results, boot_result]
        if step.get("run_ids"):
            entry["run_ids"] = step["run_ids"]
        return self._save(
            row, step={"phase": "propose"}, boot_results=boot_results, history=[*row.history, entry]
        )

    def _out_of_budget(
        self, row: TuningSessionRow, proposal: dict[str, Any], limit: str
    ) -> TuningSessionRow:
        outcome: dict[str, Any] = {"outcome": "budget", "reason": f"the budget of {limit} is spent"}
        if proposal.get("leader"):
            outcome["leader"] = proposal["leader"]
        return self._finish(row, "done", outcome, proposal)

    def _finish(
        self,
        row: TuningSessionRow,
        status: Status,
        outcome: dict[str, Any],
        proposal: dict[str, Any] | None = None,
    ) -> TuningSessionRow:
        try:
            self._managed.scale_down(row.managed_target_id)
        except Exception as error:  # the outcome stands; say the GPU may still be held
            logger.exception("tuning session %s: scale-down failed", row.id)
            outcome = {**outcome, "scale_down_error": str(error)}
        if proposal is not None and proposal.get("notes"):
            # What servtuner propose found about the inputs, such as knob values that booted the
            # same configuration, stays with the outcome.
            outcome = {**outcome, "notes": list(proposal["notes"])}
        if status == "done":
            outcome = with_pareto(
                row, with_recommendation(row, outcome, self._managed), self._runs.experiments
            )
        return self._save(
            row,
            status=status,
            outcome=outcome,
            step=None,
            finished_at=self._clock(),
            **({"proposal": proposal} if proposal is not None else {}),
        )

    def _save(self, row: TuningSessionRow, **fields: Any) -> TuningSessionRow:
        updated = self._repository.update_tuning_session(row.id, updated_at=self._clock(), **fields)
        assert updated is not None
        return updated

    def _bundle_keys(self, row: TuningSessionRow) -> list[str]:
        """This session's succeeded runs, after those of the sessions it reuses."""
        reused = list(row.request.get("reuse_from") or [])
        return [
            key
            for session_id in [*reused, row.id]
            for key in session_bundles(self._repository, self._runs.bundle_key, session_id)
        ]

    def _preset(self, reference: str) -> dict[str, Any]:
        for preset in self._presets():
            document = (
                preset.model_dump(mode="json") if hasattr(preset, "model_dump") else dict(preset)
            )
            if f"{document['name']}@{document['version']}" == reference:
                return document
        raise SessionConflictError(f"unknown workload preset {reference}")

    def _elapsed_hours(self, row: TuningSessionRow) -> float:
        return (self._clock() - row.created_at).total_seconds() / 3600
