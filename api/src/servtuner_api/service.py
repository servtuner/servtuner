"""Run creation, launching, watching and status events.

Routes stay thin and call this. The runner is the only thing here that knows
how bench is executed; nothing above it knows which runner is in use.
"""

import hashlib
import json
import logging
import threading
from collections.abc import Iterator
from typing import Any

from pydantic import BaseModel

from servtuner_api.bundles import (
    SUMMARY_FILE,
    BundleFile,
    BundleStore,
    bundle_key,
    read_bundle_json,
)
from servtuner_api.comparer import Comparer
from servtuner_api.database import RunEventRow, RunRow
from servtuner_api.deletion import (
    ExperimentDeleteImpact,
    RunDeleteImpact,
    delete_experiment,
    delete_run,
    experiment_delete_impact,
    run_delete_impact,
)
from servtuner_api.documents import parse_json_document, parse_timestamp
from servtuner_api.errors import (
    ForeignStoreError,
    InvalidBundleFileError,
    InvalidDocumentError,
    InvalidRunTokenError,
    NotAwaitingError,
)
from servtuner_api.experiments import ExperimentService
from servtuner_api.followups import followup_document, target_patch
from servtuner_api.hosts import HostService
from servtuner_api.ids import new_ulid
from servtuner_api.legacy import upgrade_bundle_document, upgrade_legacy_run_spec_raw
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.models import (
    Event,
    KnobCatalog,
    Recommendations,
    RunSpec,
    SchemaValidationError,
    SearchSpaceTemplate,
    Summary,
    WorkloadPreset,
    parse_run_spec,
    validate,
)
from servtuner_api.models.generated.bundle.recommendations_schema import Recommendation
from servtuner_api.planner import Planner
from servtuner_api.price_book import PriceBookService
from servtuner_api.records.base import (
    REWATCH_LIMIT,
    UNFINISHED_LAUNCHED,
    RunFilters,
    RunStatus,
    is_final,
    utc_now,
)
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.runs import RunResource
from servtuner_api.run_metrics import run_metric_rows
from servtuner_api.runner import LaunchRequest, RunHandle, Runner, RunnerState, RunnerStatus
from servtuner_api.scenarios import ScenarioService
from servtuner_api.schedule_service import ScheduleService, measurements
from servtuner_api.specs import SpecValidator, ValidationReport
from servtuner_api.targets.checker import TargetChecker
from servtuner_api.targets.service import TargetService
from servtuner_api.tokens import hash_run_token, new_run_token, run_token_matches, token_expiry
from servtuner_api.tuning.proposer import Proposer
from servtuner_api.tuning.service import TuningService

logger = logging.getLogger(__name__)


# How often the scheduler looks for due schedules; a slot launches within this.
DEFAULT_SCHEDULER_POLL_SECONDS = 30.0
# How often active tuning sessions advance: a step waits on deploys and runs of minutes.
DEFAULT_TUNING_POLL_SECONDS = 10.0


def _knobs_of(document: object) -> dict[str, str | int | float | bool]:
    """Knobs as submitted: the generated model would turn the integer 256 into 256.0."""
    assert isinstance(document, dict)  # parse_run_spec validated the shape
    knobs: dict[str, str | int | float | bool] = document.get("knobs", {})
    return knobs


class RunService:
    def __init__(
        self,
        repository: RunRepository,
        runner: Runner,
        store: BundleStore,
        run_token_ttl_seconds: float,
        runner_poll_seconds: float,
        planner: Planner | None = None,
        comparer: Comparer | None = None,
        validator: SpecValidator | None = None,
        scheduler_poll_seconds: float = DEFAULT_SCHEDULER_POLL_SECONDS,
        targets: TargetService | None = None,
        managed_targets: ManagedTargetService | None = None,
        proposer: Proposer | None = None,
        default_prometheus: dict[str, Any] | None = None,
        tuning_poll_seconds: float = DEFAULT_TUNING_POLL_SECONDS,
        offline: bool = False,
        image_registry: str | None = None,
    ) -> None:
        # Air-gapped installs, for GET /run-defaults.
        self.offline = offline
        self.image_registry = image_registry
        self._scheduler_poll_seconds = scheduler_poll_seconds
        self.default_prometheus = default_prometheus
        self._validator = validator or SpecValidator(None)
        self._repository = repository
        self.targets = targets or TargetService(
            repository,
            None,
            TargetChecker(None, None, None, None, checked_from="the api process"),
        )
        self.managed_targets = managed_targets or ManagedTargetService(repository, ())
        self.hosts = HostService(repository)
        self.price_book = PriceBookService(repository, offline=offline)
        self.schedules = ScheduleService(repository, self.create_run)
        self.experiments = ExperimentService(
            repository, store, comparer or Comparer(None, store), self.resources
        )
        self.scenarios = ScenarioService(repository, store, planner or Planner(None, store))
        self._tuning_poll_seconds = tuning_poll_seconds
        self.tuning = TuningService(
            repository,
            self,
            self.managed_targets,
            proposer or Proposer(None, store),
            self.workload_presets,
            default_prometheus,
        )
        self._runner = runner
        self._store = store
        self._run_token_ttl_seconds = run_token_ttl_seconds
        self._runner_poll_seconds = runner_poll_seconds
        self._stopping = threading.Event()
        self._watchers: list[threading.Thread] = []

    def create_run(self, spec_raw: bytes) -> RunRow:
        """Validate, store the submitted bytes verbatim, and launch."""
        document = parse_json_document(spec_raw)
        try:
            spec = parse_run_spec(document)
        except SchemaValidationError as error:
            raise InvalidDocumentError(error.violations) from error
        run = self._build_row(spec, document, spec_raw)
        self._repository.insert(run)
        self._launch(run.id, spec_raw)
        return self._repository.get(run.id) or run

    def list_runs(self, filters: RunFilters, limit: int) -> list[RunRow]:
        return self._repository.list_runs(filters, limit)

    def get_run(self, run_id: str) -> RunRow | None:
        return self._repository.get(run_id)

    def list_files(self, run: RunRow) -> list[BundleFile]:
        return self._store.list_files(bundle_key(self._store, run))

    def open_file(self, run: RunRow, path: str) -> str | Iterator[bytes] | None:
        """A presigned URL to redirect to, the file's chunks, or None when absent."""
        key = bundle_key(self._store, run)
        return self._store.presign(key, path) or self._store.stream(key, path)

    def list_events(self, run_id: str) -> list[RunEventRow]:
        return self._repository.list_events(run_id)

    def resource(self, run: RunRow) -> RunResource:
        return self.resources([run])[0]

    def resources(self, runs: list[RunRow]) -> list[RunResource]:
        """The runs as resources, with their usable SLO-max read from run_metrics in one query."""
        measured = measurements(self._repository, [run.id for run in runs])
        usernames = self._repository.usernames({run.created_by for run in runs if run.created_by})
        return [
            RunResource.from_row(
                run,
                self._store.uri(run.bundle_key),
                measured[run.id].slo_max_rps if measured[run.id].is_usable else None,
                usernames.get(run.created_by) if run.created_by else None,
            )
            for run in runs
        ]

    def read_document[T: BaseModel](
        self, run: RunRow, path: str, schema_name: str, model: type[T]
    ) -> T | None:
        """A schema-valid bundle document, or None when the bundle has no such file.

        A file that exists but breaks its schema raises InvalidBundleFileError:
        unlike a missing file, that is a fault worth surfacing.
        """
        document = read_bundle_json(self._store, bundle_key(self._store, run), path)
        if document is None:
            return None
        # A bundle servtuner 0.1 wrote is read in today's shape (legacy.py).
        document = upgrade_bundle_document(schema_name, document)
        try:
            validate(schema_name, document)
        except SchemaValidationError as error:
            raise InvalidBundleFileError(path, error.violations) from error
        return model.model_validate(document)

    def read_summary(self, run: RunRow) -> Summary | None:
        run_id = run.id
        try:
            document = read_bundle_json(self._store, bundle_key(self._store, run), SUMMARY_FILE)
            if document is None:
                return None
            validate("summary", document)
            return Summary.model_validate(document)
        except ValueError, OSError, ForeignStoreError:
            logger.warning("run %s has an unreadable %s", run_id, SUMMARY_FILE, exc_info=True)
            return None

    def index_metrics(self, run: RunRow) -> int:
        """Replace the run's run_metrics rows from its bundle; return how many (run_metrics.py)."""
        rows = run_metric_rows(self._store, run)
        if rows is None:
            return 0
        self._repository.replace_metrics(run.id, rows)
        return len(rows)

    def backfill_metrics(self) -> None:
        """Index every finished run that has no rows yet, such as runs from before indexing."""
        try:
            runs = self._repository.final_runs_without_metrics()
        except Exception:  # no database yet: the next start retries
            logger.exception("listing runs to index failed")
            return
        for run in runs:
            if self._stopping.is_set():
                return
            try:
                self.index_metrics(run)
            except Exception:  # one bad run must not stop the rest
                logger.exception("indexing run %s failed", run.id)

    def start(self) -> None:
        """Start background work: watchers for runs left unfinished by an earlier api process,
        the run_metrics backfill, the scheduler and tuning sessions."""
        for work in (
            self._rewatch_unfinished,
            self.backfill_metrics,
            self._schedule_loop,
            self._tuning_loop,
        ):
            thread = threading.Thread(target=work, daemon=True)
            self._watchers.append(thread)
            thread.start()

    def _schedule_loop(self) -> None:
        while not self._stopping.wait(self._scheduler_poll_seconds):
            try:
                self.schedules.run_due(utc_now())
            except Exception:  # no database yet, or a transient error: the next tick retries
                logger.exception("launching due schedules failed")

    def _tuning_loop(self) -> None:
        while not self._stopping.wait(self._tuning_poll_seconds):
            try:
                self.tuning.tick_all()
            except Exception:  # no database yet, or a transient error: the next tick retries
                logger.exception("advancing tuning sessions failed")

    def run_datasheet(self, run_id: str, token: str) -> bytes | None:
        """The price book's uploaded datasheet for a run's bench, with its run token."""
        run = self._repository.get(run_id)
        if run is None or not run_token_matches(token, run.token_hash, run.token_expires_at):
            raise InvalidRunTokenError(run_id)
        return self.price_book.datasheet_content()

    def record_event(self, run_id: str, token: str, raw_body: bytes) -> bool:
        """Authenticate with the run token, then validate and store the event."""
        run = self._repository.get(run_id)
        if run is None or not run_token_matches(token, run.token_hash, run.token_expires_at):
            raise InvalidRunTokenError(run_id)
        document = parse_json_document(raw_body)
        try:
            validate("events", document)
        except SchemaValidationError as error:
            raise InvalidDocumentError(error.violations) from error
        event = Event.model_validate(document)
        if event.run_id != run_id:
            raise InvalidDocumentError(
                [f"run_id {event.run_id!r} does not match the URL's {run_id!r}"]
            )
        try:
            event_time = parse_timestamp(event.time.root)
        except ValueError as error:
            raise InvalidDocumentError([f"time {event.time.root!r}: {error}"]) from error
        return self._repository.record_event(event, event_time)

    def bundle_key(self, run: RunRow) -> str:
        """The bundle key of a run, for tuning sessions staging bundles (tuning.RunOps)."""
        return bundle_key(self._store, run)

    def stop(self) -> None:
        self._stopping.set()
        for watcher in self._watchers:
            watcher.join()

    def create_followup(
        self, run: RunRow, recommendation_id: str, repeats: int, experiment: str | None = None
    ) -> tuple[list[RunRow], Recommendation, str | None]:
        """Create runs that test one knob recommendation (followups.py), waiting
        (awaiting_config) until the user confirms the change is applied. Returns the runs, the
        recommendation, and the target patch to apply."""
        recommendations = self.read_document(
            run, "recommendations.json", "recommendations", Recommendations
        )
        document, recommendation = followup_document(
            run, recommendations, recommendation_id, experiment
        )
        runs = []
        for _ in range(repeats):
            spec_raw = json.dumps(document).encode()
            try:
                spec = parse_run_spec(json.loads(spec_raw))
            except SchemaValidationError as error:
                raise InvalidDocumentError(error.violations) from error
            row = self._build_row(spec, json.loads(spec_raw), spec_raw)
            row.status = RunStatus.AWAITING_CONFIG.value
            self._repository.insert(row)
            runs.append(row)
        return runs, recommendation, target_patch(self._store, run)

    def validate_spec(self, spec_text: bytes) -> ValidationReport:
        """A dry run of servtuner validate on the submitted RunSpec text."""
        return self._validator.validate(spec_text)

    def workload_presets(self) -> list[WorkloadPreset]:
        """The workload presets the bench binary carries."""
        return self._validator.workload_presets()

    def knob_catalog(self) -> KnobCatalog:
        """Every vLLM flag as a knob, from vLLM's source at the pinned version."""
        return self._validator.knob_catalog()

    def search_space_templates(self) -> list[SearchSpaceTemplate]:
        """The starting search spaces the bench binary carries."""
        return self._validator.search_space_templates()

    def confirm_applied(self, run_id: str) -> RunRow:
        """The user applied the change a follow-up run tests: launch it (its run token is minted
        at launch)."""
        released = self._repository.release_awaiting(run_id)
        run = self._repository.get(run_id)
        if not released or run is None:
            raise NotAwaitingError(f"run {run_id} is not waiting for a config change")
        # A run created by servtuner 0.1 kept its v1 RunSpec; bench reads only v2.
        self._launch(run.id, upgrade_legacy_run_spec_raw(run.spec_raw))
        return self._repository.get(run_id) or run

    def _build_row(self, spec: RunSpec, document: object, spec_raw: bytes) -> RunRow:
        run_id = new_ulid()
        return RunRow(
            id=run_id,
            name=spec.name,
            experiment=spec.experiment,
            status=RunStatus.PENDING.value,
            target_type=spec.target.type.value,
            knobs=_knobs_of(document),
            labels=dict(spec.labels),
            spec_raw=spec_raw,
            spec_hash=hashlib.sha256(spec_raw).hexdigest(),
            bundle_store=self._store.name,
            bundle_key=self._store.key(run_id),
            created_at=utc_now(),
        )

    def run_delete_impact(self, run: RunRow) -> RunDeleteImpact:
        return run_delete_impact(self._repository, self._store, run)

    def delete_run(self, run: RunRow) -> RunDeleteImpact:
        """A user's delete: raises DeleteBlockedError while anything needs the run."""
        return delete_run(self._repository, self._store, run)

    def experiment_delete_impact(self, name: str) -> ExperimentDeleteImpact:
        return experiment_delete_impact(self._repository, self._store, name)

    def delete_experiment(self, name: str, *, delete_scenarios: bool) -> ExperimentDeleteImpact:
        """A user's delete: raises DeleteBlockedError while anything needs it."""
        return delete_experiment(
            self._repository, self._store, name, delete_scenarios=delete_scenarios
        )

    def cancel_run(self, run_id: str) -> RunRow | None:
        """Stop the run's bench and mark it cancelled; a finished run is left as it is.

        The runner stops bench (SIGTERM to the process, or the Job deleted), and bench finalizes
        the bundle with what it collected. The run is marked cancelled at once, so a runner that
        no longer knows the run (the subprocess runner after an api restart) cannot leave it
        running. None when there is no such run.
        """
        run = self._repository.get(run_id)
        if run is None or is_final(run.status):
            return run
        self._runner.cancel(RunHandle(run_id, run_id))
        self._repository.apply_runner_status(
            run_id, RunnerStatus(RunnerState.CANCELLED, message="cancelled")
        )
        return self._repository.get(run_id)

    def _rewatch_unfinished(self) -> None:
        """Watch the runs an earlier api process launched and did not see end. Both runners
        answer from the run id: the Kubernetes runner finds the Job, and the subprocess runner,
        whose children ended with that process, reports the run failed with why. It retries
        until the database answers, as the other startup work does: the api must start without
        one."""
        while not self._stopping.is_set():
            try:
                self._watch_unfinished()
                return
            except Exception:  # no database yet: try again
                logger.exception("finding unfinished runs failed")
            self._stopping.wait(self._runner_poll_seconds)

    def _watch_unfinished(self) -> None:
        for status in UNFINISHED_LAUNCHED:
            for run in self._repository.list_runs(RunFilters(status=status), REWATCH_LIMIT):
                watcher = threading.Thread(
                    target=self._watch, args=(RunHandle(run.id, run.id),), daemon=True
                )
                self._watchers.append(watcher)
                watcher.start()

    def _launch(self, run_id: str, spec_raw: bytes) -> None:
        """Launch the run's job with a run token minted now: unique to this launch, so a
        relaunch's token replaces the last one, and a run that never launched has none."""
        token = new_run_token()
        self._repository.mark_launching(
            run_id, hash_run_token(token), token_expiry(self._run_token_ttl_seconds)
        )
        try:
            handle = self._runner.launch(LaunchRequest(run_id, spec_raw, token))
        except Exception as error:  # a failed launch must end the run, whatever went wrong
            logger.exception("launching run %s failed", run_id)
            self._repository.mark_launch_failed(run_id, f"launch failed: {error}")
            return
        watcher = threading.Thread(target=self._watch, args=(handle,), daemon=True)
        self._watchers.append(watcher)
        watcher.start()

    def _index_finished(self, run_id: str) -> None:
        """Index a run that just ended; the backfill retries it after a failure here."""
        try:
            run = self._repository.get(run_id)
            if run is not None:
                self.index_metrics(run)
        except Exception:  # the run has ended: nothing here may keep its watcher polling
            logger.exception("indexing run %s failed", run_id)

    def _watch(self, handle: RunHandle) -> None:
        """Poll the runner until it reports a final state; that state wins."""
        while not self._stopping.is_set():
            try:
                status = self._runner.status(handle)
                self._repository.apply_runner_status(handle.run_id, status)
                if status.is_final:
                    self._index_finished(handle.run_id)
                    return
            except Exception:  # keep watching: a transient database error must not orphan the run
                logger.exception("polling run %s failed", handle.run_id)
            self._stopping.wait(self._runner_poll_seconds)
