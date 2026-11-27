"""Comparisons and experiments."""

from dataclasses import asdict
from datetime import datetime
from typing import TYPE_CHECKING

from pydantic import BaseModel, ConfigDict

from servtuner_api.comparison import Alignment
from servtuner_api.database import ExperimentAnalysisRow
from servtuner_api.models import Comparison
from servtuner_api.models.generated.comparison_schema import ObjectiveName
from servtuner_api.records.base import ExperimentListing
from servtuner_api.resources.runs import RunResource

if TYPE_CHECKING:  # deletion imports tuning, which imports this module
    from servtuner_api.deletion import ExperimentDeleteImpact


class ScenarioRefResource(BaseModel):
    id: str
    name: str


class ExperimentDeleteImpactResource(BaseModel):
    """What deleting an experiment would do: its runs, analyses and finished tuning
    sessions go; scenarios built on its runs go only when the delete asks for them."""

    name: str
    blocks: list[str]
    runs: list[str]
    analyses: int
    bundle_files: int
    sessions: list[str]
    scenarios: list[ScenarioRefResource]
    notes: list[str]

    @classmethod
    def of(cls, impact: ExperimentDeleteImpact) -> ExperimentDeleteImpactResource:
        return cls.model_validate(asdict(impact))


class KnobComparison(BaseModel):
    """One knob across the compared runs: null where a run does not declare it."""

    model_config = ConfigDict(extra="forbid")

    name: str
    values: list[str | int | float | bool | None]
    differs: bool


class MetricComparison(BaseModel):
    """One run_metrics cell across the compared runs: null where a run has no value.

    ``stage`` is a stage index, or ``run`` for run-level values such as
    SLO-max RPS; the names are those of the run_metrics index.
    """

    model_config = ConfigDict(extra="forbid")

    stage: str
    metric: str
    stat: str
    values: list[float | None]


class ComparisonResource(BaseModel):
    """Runs lined up by knob and by stage. Every list of values is in ``runs`` order.

    It aligns values and does not rank them: stages are matched by index, which
    only compares like with like when ``ladders_match``. Runs that have not been
    indexed (no analysis.json) are listed in ``unindexed_run_ids``.
    """

    model_config = ConfigDict(extra="forbid")

    runs: list[RunResource]
    knobs: list[KnobComparison]
    metrics: list[MetricComparison]
    unindexed_run_ids: list[str]
    ladders_match: bool

    @classmethod
    def build(cls, runs: list[RunResource], alignment: Alignment) -> ComparisonResource:
        return cls(
            runs=runs,
            knobs=[
                KnobComparison(name=knob.name, values=knob.values, differs=knob.differs)
                for knob in alignment.knobs
            ],
            metrics=[
                MetricComparison(
                    stage=metric.stage, metric=metric.metric, stat=metric.stat, values=metric.values
                )
                for metric in alignment.metrics
            ],
            unindexed_run_ids=alignment.unindexed_run_ids,
            ladders_match=alignment.ladders_match,
        )


class ExperimentResource(BaseModel):
    model_config = ConfigDict(extra="forbid")

    name: str
    created_at: datetime
    run_count: int
    latest_run_at: datetime | None = None

    @classmethod
    def from_listing(cls, listing: ExperimentListing) -> ExperimentResource:
        return cls(
            name=listing.name,
            created_at=listing.created_at,
            run_count=listing.run_count,
            latest_run_at=listing.latest_run_at,
        )


class ExperimentAnalyze(BaseModel):
    """POST /experiments/{name}/analyze. Without run_ids, every succeeded run of the
    experiment that has an analysis is compared; without objective, bench picks the
    default for the target type."""

    model_config = ConfigDict(extra="forbid")

    objective: ObjectiveName | None = None
    run_ids: list[str] | None = None


class ExperimentAnalysisResource(BaseModel):
    """One experiment comparison: servtuner compare's comparison.json, and what it compared."""

    model_config = ConfigDict(extra="forbid")

    id: str
    experiment: str
    created_at: datetime
    objective: ObjectiveName
    run_ids: list[str]
    comparison: Comparison

    @classmethod
    def from_row(cls, row: ExperimentAnalysisRow) -> ExperimentAnalysisResource:
        return cls(
            id=row.id,
            experiment=row.experiment,
            created_at=row.created_at,
            objective=ObjectiveName(row.objective),
            run_ids=row.run_ids,
            comparison=Comparison.model_validate(row.comparison),
        )


class ExperimentDetail(ExperimentResource):
    """An experiment's runs, oldest first, their comparison, and its latest analysis."""

    varied_knobs: list[str]
    comparison: ComparisonResource
    latest_analysis: ExperimentAnalysisResource | None = None
