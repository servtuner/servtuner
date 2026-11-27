"""Line runs up for GET /compare and GET /experiments/{name} (api/CLAUDE.md).

This is a view over run_metrics and the runs' declared knobs: it aligns
values, it does not judge them. Which configuration wins, with CIs across
repeats, is experiment analysis, and all analysis lives in bench.

Stages are aligned by index. That is only meaningful when every run offered
the same load at each index, so the comparison says whether the ladders match.
"""

import math
from collections.abc import Sequence
from dataclasses import dataclass

from servtuner_api.database import RunMetricRow, RunRow
from servtuner_api.indexing import RUN_STAGE, VALUE

KnobValue = str | int | float | bool
OFFERED_RPS = "offered_rps"
# Offered rates are equal when they differ by less than this fraction: a
# ladder written as 50 and one computed as 49.99999 offer the same load.
LADDER_RELATIVE_TOLERANCE = 1e-6


@dataclass(frozen=True)
class AlignedKnob:
    name: str
    values: list[KnobValue | None]
    differs: bool


@dataclass(frozen=True)
class AlignedMetric:
    stage: str
    metric: str
    stat: str
    values: list[float | None]


@dataclass(frozen=True)
class Alignment:
    knobs: list[AlignedKnob]
    metrics: list[AlignedMetric]
    unindexed_run_ids: list[str]
    ladders_match: bool


def align(runs: Sequence[RunRow], rows: Sequence[RunMetricRow]) -> Alignment:
    """Align the runs' knobs and metrics; each list of values is in the runs' order."""
    position = {run.id: index for index, run in enumerate(runs)}
    cells: dict[tuple[str, str, str], list[float | None]] = {}
    for row in rows:
        empty: list[float | None] = [None] * len(runs)
        values = cells.setdefault((row.stage, row.metric, row.stat), empty)
        values[position[row.run_id]] = row.value
    indexed = {row.run_id for row in rows}
    unindexed = [run.id for run in runs if run.id not in indexed]
    metrics = [
        AlignedMetric(stage, metric, stat, values)
        for (stage, metric, stat), values in sorted(cells.items(), key=_cell_order)
    ]
    return Alignment(
        knobs=align_knobs(runs),
        metrics=metrics,
        unindexed_run_ids=unindexed,
        ladders_match=_ladders_match(cells, [position[run_id] for run_id in sorted(indexed)]),
    )


def align_knobs(runs: Sequence[RunRow]) -> list[AlignedKnob]:
    """Every knob any run declares; a knob a run does not declare is None for it."""
    names = sorted({name for run in runs for name in run.knobs})
    knobs = []
    for name in names:
        values = [run.knobs.get(name) for run in runs]
        knobs.append(AlignedKnob(name, values, differs=len({repr(value) for value in values}) > 1))
    return knobs


def _cell_order(item: tuple[tuple[str, str, str], object]) -> tuple[int, int, str, str]:
    """Run-level values first, then stages in numeric order, then metric and stat."""
    stage, metric, stat = item[0]
    if stage == RUN_STAGE:
        return (0, 0, metric, stat)
    return (1, int(stage), metric, stat)


def _ladders_match(
    cells: dict[tuple[str, str, str], list[float | None]], indexed_positions: list[int]
) -> bool:
    """True when every indexed run has the same stages, offering the same RPS at each."""
    for (_, metric, stat), values in cells.items():
        if metric != OFFERED_RPS or stat != VALUE:
            continue
        offered = [values[position] for position in indexed_positions]
        present = [value for value in offered if value is not None]
        if len(present) != len(offered):
            return False
        if any(
            not math.isclose(value, present[0], rel_tol=LADDER_RELATIVE_TOLERANCE)
            for value in present
        ):
            return False
    return True
