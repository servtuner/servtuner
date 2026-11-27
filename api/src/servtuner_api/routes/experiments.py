"""Comparing runs and experiments."""

from typing import Annotated

from fastapi import APIRouter, HTTPException, Query, status

from servtuner_api.comparer import CompareError, ComparerUnavailableError, MissingBundleFileError
from servtuner_api.deletion import DeleteBlockedError
from servtuner_api.errors import ForeignStoreError, NoAnalysableRunsError, UnknownRunsError
from servtuner_api.ids import ULID_PATTERN
from servtuner_api.resources.experiments import (
    ComparisonResource,
    ExperimentAnalysisResource,
    ExperimentAnalyze,
    ExperimentDeleteImpactResource,
    ExperimentDetail,
    ExperimentResource,
)
from servtuner_api.routes.common import Service, blocked, experiment_or_404

# The most runs one GET /compare lines up; an experiment's own view has no cap.
MAX_COMPARED_RUNS = 50

router = APIRouter()


def _compared_run_ids(runs: str) -> list[str]:
    run_ids = [run_id.strip() for run_id in runs.split(",") if run_id.strip()]
    problems = []
    if not run_ids:
        problems.append("runs must name at least one run")
    if len(run_ids) > MAX_COMPARED_RUNS:
        problems.append(f"runs may name at most {MAX_COMPARED_RUNS} runs")
    if len(set(run_ids)) != len(run_ids):
        problems.append("runs names a run more than once")
    if problems:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "; ".join(problems))
    return run_ids


@router.get("/compare", response_model_exclude_none=True)
def compare(
    service: Service,
    runs: Annotated[str, Query(description="comma-separated run ids, in display order")],
) -> ComparisonResource:
    """Runs lined up by knob and stage, from run_metrics. Aligns; does not rank."""
    run_ids = _compared_run_ids(runs)
    unknown = [run_id for run_id in run_ids if not ULID_PATTERN.match(run_id)]
    try:
        if unknown:
            raise UnknownRunsError(unknown)
        return service.experiments.compare(run_ids)
    except UnknownRunsError as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error


@router.get("/experiments", response_model_exclude_none=True)
def list_experiments(service: Service) -> list[ExperimentResource]:
    return service.experiments.list_all()


@router.get("/experiments/{name}", response_model_exclude_none=True)
def get_experiment(name: str, service: Service) -> ExperimentDetail:
    experiment = service.experiments.get(name)
    if experiment is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"experiment {name} not found")
    return experiment


@router.get("/experiments/{name}/delete-impact")
def experiment_delete_impact(name: str, service: Service) -> ExperimentDeleteImpactResource:
    """What deleting the experiment would remove, and what blocks it."""
    experiment_or_404(service, name)
    return ExperimentDeleteImpactResource.of(service.experiment_delete_impact(name))


@router.delete("/experiments/{name}")
def delete_experiment(
    name: str, service: Service, delete_scenarios: bool = False
) -> ExperimentDeleteImpactResource:
    """Delete an experiment with its runs, analyses and finished tuning sessions; only ever a
    user's request. delete_scenarios also removes the saved scenarios built
    on its runs, which otherwise block. 409 with the blocks."""
    experiment_or_404(service, name)
    try:
        return ExperimentDeleteImpactResource.of(
            service.delete_experiment(name, delete_scenarios=delete_scenarios)
        )
    except DeleteBlockedError as error:
        raise blocked("the experiment cannot be deleted", error) from error


@router.post(
    "/experiments/{name}/analyze",
    status_code=status.HTTP_201_CREATED,
    response_model_exclude_none=True,
)
def analyze_experiment(
    name: str, service: Service, body: ExperimentAnalyze | None = None
) -> ExperimentAnalysisResource:
    """Compare the experiment's analysed runs with servtuner compare."""
    try:
        analysis = service.experiments.analyze(name, body or ExperimentAnalyze())
    except UnknownRunsError as error:
        raise HTTPException(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            f"not succeeded, analysed runs of experiment {name}: {', '.join(error.run_ids)}",
        ) from error
    except NoAnalysableRunsError as error:
        raise HTTPException(status.HTTP_409_CONFLICT, str(error)) from error
    except (MissingBundleFileError, ForeignStoreError) as error:
        raise HTTPException(status.HTTP_404_NOT_FOUND, str(error)) from error
    except CompareError as error:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(error)) from error
    except ComparerUnavailableError as error:
        raise HTTPException(status.HTTP_503_SERVICE_UNAVAILABLE, str(error)) from error
    if analysis is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, f"experiment {name} not found")
    return analysis
