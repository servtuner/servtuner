"""Scheduled regression runs: slots, labels and verdicts.

A schedule launches its RunSpec at ``start_at + k * every_seconds``. A slot
that falls due while the schedule's previous run is still going launches
nothing (runs of one target must not overlap, or each measures the other);
slots missed while the api was down are skipped, never replayed in a burst.

Each run's verdict compares its SLO-max RPS with the baseline's: a drop of
more than ``regression_tolerance`` (10% by default, the analysis's linearity
tolerance) is a regression, a rise of more than it an improvement.
"""

import json
from dataclasses import dataclass
from datetime import datetime, timedelta
from enum import StrEnum

# The label each scheduled run carries: its schedule's id.
SCHEDULE_LABEL = "servtuner.schedule"
DEFAULT_REGRESSION_TOLERANCE = 0.10
MIN_EVERY_SECONDS = 3600  # one hour: a run with warmup and a ladder takes minutes
MAX_EVERY_SECONDS = 30 * 24 * 3600  # thirty days


class Verdict(StrEnum):
    PENDING = "pending"  # not finished yet
    FAILED = "failed"  # failed or cancelled: no measurement
    UNMEASURED = "unmeasured"  # no SLO-max, or excluded from comparison
    BASELINE = "baseline"
    NO_BASELINE = "no_baseline"
    STEADY = "steady"
    IMPROVED = "improved"
    REGRESSED = "regressed"


@dataclass(frozen=True)
class Measurement:
    """A finished run's indexed numbers: SLO-max RPS when it has one."""

    slo_max_rps: float | None
    is_excluded: bool

    @property
    def is_usable(self) -> bool:
        return self.slo_max_rps is not None and not self.is_excluded


def next_slot(due: datetime, every_seconds: int, now: datetime) -> datetime:
    """The first slot after ``now``, counting from the slot that fell due."""
    every = timedelta(seconds=every_seconds)
    missed = (now - due) // every
    return due + (missed + 1) * every


def first_slot(start_at: datetime, every_seconds: int, now: datetime) -> datetime:
    """``start_at`` when it is still ahead, else the next slot of its series."""
    return start_at if start_at > now else next_slot(start_at, every_seconds, now)


def labelled_spec(spec_raw: bytes, schedule_id: str) -> bytes:
    """The RunSpec with the schedule label added, so the run is found by label."""
    document = json.loads(spec_raw)
    document["labels"] = {**document.get("labels", {}), SCHEDULE_LABEL: schedule_id}
    return json.dumps(document).encode()


def verdict(
    status: str,
    is_final: bool,
    measurement: Measurement,
    baseline: float | None,
    is_baseline: bool,
    tolerance: float,
) -> tuple[Verdict, float | None]:
    """The run's verdict and its SLO-max change against the baseline, as a fraction."""
    if not is_final:
        return Verdict.PENDING, None
    if status != "succeeded":
        return Verdict.FAILED, None
    if not measurement.is_usable or measurement.slo_max_rps is None:
        return Verdict.UNMEASURED, None
    if is_baseline:
        return Verdict.BASELINE, 0.0
    if baseline is None or baseline <= 0:
        return Verdict.NO_BASELINE, None
    change = measurement.slo_max_rps / baseline - 1
    if change < -tolerance:
        return Verdict.REGRESSED, change
    if change > tolerance:
        return Verdict.IMPROVED, change
    return Verdict.STEADY, change
