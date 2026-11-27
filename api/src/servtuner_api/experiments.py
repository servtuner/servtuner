"""Comparing runs and experiments."""

from collections.abc import Callable

from servtuner_api.bundles import BundleStore, bundle_key
from servtuner_api.comparer import Comparer
from servtuner_api.comparison import align
from servtuner_api.database import ExperimentAnalysisRow, ExperimentRow, RunRow
from servtuner_api.errors import NoAnalysableRunsError, UnknownRunsError
from servtuner_api.ids import new_ulid
from servtuner_api.models import validate
from servtuner_api.records.base import utc_now
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.experiments import (
    ComparisonResource,
    ExperimentAnalysisResource,
    ExperimentAnalyze,
    ExperimentDetail,
    ExperimentResource,
)
from servtuner_api.resources.runs import RunResource


class ExperimentService:
    """Comparing runs and experiments: aligned from run_metrics, analysed by
    servtuner compare."""

    def __init__(
        self,
        repository: RunRepository,
        store: BundleStore,
        comparer: Comparer,
        resources: Callable[[list[RunRow]], list[RunResource]],
    ) -> None:
        self._repository = repository
        self._store = store
        self._comparer = comparer
        self._resources = resources

    def compare(self, run_ids: list[str]) -> ComparisonResource:
        """Line the runs up in the order given; UnknownRunsError names any that do not exist."""
        runs = {run_id: self._repository.get(run_id) for run_id in run_ids}
        missing = [run_id for run_id, run in runs.items() if run is None]
        if missing:
            raise UnknownRunsError(missing)
        return self._comparison([run for run in runs.values() if run is not None])

    def list_all(self) -> list[ExperimentResource]:
        return [ExperimentResource.from_listing(e) for e in self._repository.list_experiments()]

    def get_row(self, name: str) -> ExperimentRow | None:
        return self._repository.get_experiment(name)

    def get(self, name: str) -> ExperimentDetail | None:
        experiment = self._repository.get_experiment(name)
        if experiment is None:
            return None
        runs = self._repository.runs_of_experiment(name)
        comparison = self._comparison(runs)
        latest = self._repository.latest_analysis(name)
        return ExperimentDetail(
            name=experiment.name,
            created_at=experiment.created_at,
            run_count=len(runs),
            latest_run_at=max((run.created_at for run in runs), default=None),
            varied_knobs=[knob.name for knob in comparison.knobs if knob.differs],
            comparison=comparison,
            latest_analysis=ExperimentAnalysisResource.from_row(latest) if latest else None,
        )

    def analyze(self, name: str, request: ExperimentAnalyze) -> ExperimentAnalysisResource | None:
        """Run servtuner compare over the experiment's analysed runs and keep the result.

        Returns None for an unknown experiment. UnknownRunsError names requested
        runs that are not analysed runs of it; NoAnalysableRunsError means there
        is nothing to compare.
        """
        if self._repository.get_experiment(name) is None:
            return None
        runs = self._repository.indexed_runs_of_experiment(name)
        if request.run_ids is not None:
            available = {run.id: run for run in runs}
            unknown = [run_id for run_id in request.run_ids if run_id not in available]
            if unknown:
                raise UnknownRunsError(unknown)
            runs = [available[run_id] for run_id in dict.fromkeys(request.run_ids)]
        if not runs:
            raise NoAnalysableRunsError(
                f"experiment {name} has no succeeded run with an analysis.json to compare"
            )
        objective = request.objective.value if request.objective else None
        bundles = {run.id: bundle_key(self._store, run) for run in runs}
        document = self._comparer.compare(bundles, objective)
        validate("comparison", document)
        assert isinstance(document, dict)  # the schema's root is an object
        row = ExperimentAnalysisRow(
            id=new_ulid(),
            experiment=name,
            created_at=utc_now(),
            objective=document["objective"],
            run_ids=list(bundles),
            comparison=document,
        )
        self._repository.insert_analysis(row)
        return ExperimentAnalysisResource.from_row(row)

    def _comparison(self, runs: list[RunRow]) -> ComparisonResource:
        rows = self._repository.list_metrics([run.id for run in runs])
        return ComparisonResource.build(self._resources(runs), align(runs, rows))
