"""Deleting runs and experiments. Only a user starts a delete: nothing
in servtuner deletes runs on its own, and every delete is preceded by its impact, which names what
blocks it, what goes with it and what is kept but marked.

A run: blocked while it is unfinished, while a schedule uses it as its baseline, while an
active tuning session is running it, and while a saved scenario is built on it. The experiment
analyses that included it go with it (they could not be recomputed). A finished tuning session
keeps its recommendation; its history then shows the run as deleted.

An experiment: its runs (each blocked as above), its analyses, its row, and the finished
tuning sessions whose experiment it is; refused while any of them is active. Saved scenarios built
on its runs block it unless the user asks for them to be deleted too. Managed targets and
schedules are never deleted.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from servtuner_api.bundles import BundleStore
from servtuner_api.database import RunRow, ScenarioRow, ScheduleRow, TuningSessionRow
from servtuner_api.records.base import ACTIVE_SESSION_STATUSES, FINAL_STATUSES
from servtuner_api.records.repository import RunRepository
from servtuner_api.tuning.models import SESSION_LABEL

KEPT_SESSION_NOTE = (
    "tuning session {experiment} keeps its recommendation; its history will show {what} as deleted"
)


@dataclass(frozen=True)
class RunDeleteImpact:
    """What deleting a run would do, before it is done."""

    run_id: str
    # Why it cannot be deleted now; empty when it can.
    blocks: list[str] = field(default_factory=list)
    # Experiment analyses that included it, deleted with it.
    analyses: list[str] = field(default_factory=list)
    bundle_files: int = 0
    # What stays but changes meaning (a finished session's history entry).
    notes: list[str] = field(default_factory=list)


@dataclass(frozen=True)
class ScenarioRef:
    id: str
    name: str


@dataclass(frozen=True)
class ExperimentDeleteImpact:
    """What deleting an experiment would do, before it is done."""

    name: str
    blocks: list[str] = field(default_factory=list)
    runs: list[str] = field(default_factory=list)
    analyses: int = 0
    bundle_files: int = 0
    # Finished tuning sessions of the experiment, deleted with it.
    sessions: list[str] = field(default_factory=list)
    # Saved scenarios built on its runs: they block unless the user asks to delete them too.
    scenarios: list[ScenarioRef] = field(default_factory=list)
    notes: list[str] = field(default_factory=list)


class DeleteBlockedError(RuntimeError):
    """Something still needs what was to be deleted; the blocks say what and how to free it."""

    def __init__(self, blocks: list[str]) -> None:
        super().__init__("; ".join(blocks))
        self.blocks = blocks


@dataclass(frozen=True)
class _World:
    """What can refer to a run, read once per impact."""

    schedules: list[ScheduleRow]
    sessions: list[TuningSessionRow]
    scenarios: list[ScenarioRow]

    @classmethod
    def read(cls, repository: RunRepository) -> _World:
        return cls(
            repository.list_schedules(),
            repository.list_tuning_sessions(),
            repository.list_scenarios(),
        )


@dataclass(frozen=True)
class _RunChecks:
    """A run's own blocks, plus the session and scenarios its callers weigh differently."""

    blocks: list[str]
    session: TuningSessionRow | None
    scenarios: list[ScenarioRow]
    bundle_files: int

    @property
    def session_active(self) -> bool:
        return self.session is not None and self.session.status in ACTIVE_SESSION_STATUSES


def _check_run(store: BundleStore, world: _World, run: RunRow) -> _RunChecks:
    blocks: list[str] = []
    if run.status not in FINAL_STATUSES:
        blocks.append(f"it is {run.status}: cancel it first")
    foreign = run.bundle_store != store.name
    if foreign:
        blocks.append(
            f"its bundle is in store {run.bundle_store!r}, which this api does not have, so it "
            "could not be removed"
        )
    for schedule in world.schedules:
        if schedule.baseline_run_id == run.id:
            blocks.append(f"schedule {schedule.name} uses it as its baseline: choose another first")
    session_id = run.labels.get(SESSION_LABEL)
    session = next((s for s in world.sessions if session_id and s.id.lower() == session_id), None)
    scenarios = [
        scenario
        for scenario in world.scenarios
        if run.id in {*scenario.body.get("run_ids", []), *scenario.body.get("payload_run_ids", [])}
    ]
    return _RunChecks(
        blocks=blocks,
        session=session,
        scenarios=scenarios,
        bundle_files=0 if foreign else len(store.list_files(run.bundle_key)),
    )


def run_delete_impact(
    repository: RunRepository, store: BundleStore, run: RunRow
) -> RunDeleteImpact:
    checks = _check_run(store, _World.read(repository), run)
    blocks = list(checks.blocks)
    notes: list[str] = []
    if checks.session is not None:
        if checks.session_active:
            blocks.append(f"tuning session {checks.session.experiment} is still running it")
        else:
            notes.append(
                KEPT_SESSION_NOTE.format(experiment=checks.session.experiment, what="this run")
            )
    blocks += [
        f"saved scenario {scenario.name} is built on it: delete the scenario first"
        for scenario in checks.scenarios
    ]
    return RunDeleteImpact(
        run_id=run.id,
        blocks=blocks,
        analyses=[analysis.id for analysis in repository.analyses_including(run.id)],
        bundle_files=checks.bundle_files,
        notes=notes,
    )


def delete_run(repository: RunRepository, store: BundleStore, run: RunRow) -> RunDeleteImpact:
    """Delete a run a user asked to delete: refuse while anything blocks it, then the bundle,
    then the analyses and the row in one transaction. The bundle goes first so a failure leaves
    a row that a retry deletes again (a missing bundle is not an error), never a dangling bundle."""
    impact = run_delete_impact(repository, store, run)
    if impact.blocks:
        raise DeleteBlockedError(impact.blocks)
    _remove_run(repository, store, run)
    return impact


def _remove_run(repository: RunRepository, store: BundleStore, run: RunRow) -> None:
    store.delete(run.bundle_key)
    repository.delete_run(run.id, [a.id for a in repository.analyses_including(run.id)])


def experiment_delete_impact(
    repository: RunRepository, store: BundleStore, name: str
) -> ExperimentDeleteImpact:
    world = _World.read(repository)
    runs = repository.runs_of_experiment(name)
    own_sessions = [s for s in world.sessions if s.experiment == name]
    blocks = [
        f"tuning session {s.experiment} is still running: cancel it first"
        for s in own_sessions
        if s.status in ACTIVE_SESSION_STATUSES
    ]
    scenarios: dict[str, ScenarioRef] = {}
    notes: list[str] = []
    bundle_files = 0
    for run in runs:
        checks = _check_run(store, world, run)
        bundle_files += checks.bundle_files
        label = run.name or run.id
        blocks += [f"run {label}: {block}" for block in checks.blocks]
        for scenario in checks.scenarios:
            scenarios.setdefault(scenario.id, ScenarioRef(scenario.id, scenario.name))
        session = checks.session
        if session is None or session in own_sessions:
            continue  # an own session's activity is already a block; finished, it goes too
        if checks.session_active:
            blocks.append(f"run {label}: tuning session {session.experiment} is still running it")
        else:
            note = KEPT_SESSION_NOTE.format(experiment=session.experiment, what="its runs here")
            if note not in notes:
                notes.append(note)
    return ExperimentDeleteImpact(
        name=name,
        blocks=blocks,
        runs=[run.id for run in runs],
        analyses=repository.count_analyses(name),
        bundle_files=bundle_files,
        sessions=[s.id for s in own_sessions],
        scenarios=list(scenarios.values()),
        notes=notes,
    )


def delete_experiment(
    repository: RunRepository, store: BundleStore, name: str, *, delete_scenarios: bool
) -> ExperimentDeleteImpact:
    """Delete an experiment a user asked to delete. Every check is made before anything
    is removed; then the scenarios (when asked), each run as delete_run does, and finally the
    sessions and the experiment row (its analyses cascade). Each step tolerates a retry."""
    impact = experiment_delete_impact(repository, store, name)
    blocks = list(impact.blocks)
    if not delete_scenarios:
        blocks += [
            f"saved scenario {scenario.name} is built on its runs: delete it with the experiment"
            " or first"
            for scenario in impact.scenarios
        ]
    if blocks:
        raise DeleteBlockedError(blocks)
    for scenario in impact.scenarios:
        repository.delete_scenario(scenario.id)
    for run in repository.runs_of_experiment(name):
        _remove_run(repository, store, run)
    repository.delete_experiment(name, impact.sessions)
    return impact
