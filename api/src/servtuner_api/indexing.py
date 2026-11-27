"""Index a finished run's analysis.json into run_metrics (api/CLAUDE.md).

run_metrics is long format, (run_id, stage, metric, stat, value), so /compare
and experiment views can line runs up without schema changes. The bundle stays
the source of truth (CLAUDE.md rule 2): this index is derived, and reindexing
replaces a run's rows wholesale.

Naming:
- ``stage`` is the stage index as a string, or ``RUN_STAGE`` for run-level
  values such as SLO-max RPS.
- Plain numbers use the stat ``value``; flags are 1.0 or 0.0.
- Latency metrics are ``<name>_seconds`` with the stats ``p50`` … ``p99``,
  ``<p>_ci_low``/``<p>_ci_high`` for their bootstrap bounds, ``mean`` and
  ``max``. A percentile with too few samples has no rows.
"""

from collections.abc import Iterator, Mapping
from dataclasses import dataclass
from typing import Any

ANALYSIS_FILE = "analysis.json"
CAPACITY_FILE = "capacity.json"
RUN_STAGE = "run"
VALUE = "value"

# Stage-aggregate numbers indexed as they are, under their own names.
STAGE_NUMBERS = (
    "offered_rps",
    "achieved_rps",
    "goodput_rps",
    "requests",
    "errors",
    "send_lag_p50_seconds",
    "send_lag_p99_seconds",
    "send_lag_max_seconds",
    "input_tokens_per_second",
    "output_tokens_per_second",
    "total_tokens_per_second",
)
LATENCIES = ("ttft", "itl", "tpot", "e2e")
PERCENTILES = ("p50", "p90", "p95", "p99")
STAGE_FLAGS = ("is_valid", "is_client_bound", "is_excluded")


@dataclass(frozen=True)
class MetricRow:
    stage: str
    metric: str
    stat: str
    value: float


def metric_rows(
    analysis: Mapping[str, Any], capacity: Mapping[str, Any] | None = None
) -> list[MetricRow]:
    """Every run_metrics row for one schema-valid analysis.json document, plus the cost bench
    priced into capacity.json when there is one."""
    rows = list(_stage_rows(analysis)) + list(_run_rows(analysis))
    return rows + list(_cost_rows(capacity)) if capacity is not None else rows


def _cost_rows(capacity: Mapping[str, Any]) -> Iterator[MetricRow]:
    """Priced costs only: an unpriced run has no row, so it is never shown as free."""
    infra = capacity.get("infra_cost") or {}
    if infra.get("is_priced") and infra.get("usd_per_replica_hour") is not None:
        yield MetricRow(
            RUN_STAGE, "infra_usd_per_replica_hour", VALUE, float(infra["usd_per_replica_hour"])
        )
        for name in ("usd_per_1k_requests", "usd_per_1m_output_tokens"):
            if infra.get(name) is not None:
                yield MetricRow(RUN_STAGE, f"infra_{name}", VALUE, float(infra[name]))
    token = capacity.get("token_cost") or {}
    if token.get("is_priced"):
        for name in ("usd_per_1k_requests", "usd_per_1m_output_tokens"):
            if token.get(name) is not None:
                yield MetricRow(RUN_STAGE, f"token_{name}", VALUE, float(token[name]))


def _stage_rows(analysis: Mapping[str, Any]) -> Iterator[MetricRow]:
    for aggregate in analysis["stages"]:
        stage = str(aggregate["stage_index"])
        for name in STAGE_NUMBERS:
            if name in aggregate:
                yield MetricRow(stage, name, VALUE, float(aggregate[name]))
        if aggregate["requests"] > 0:
            rate = aggregate["errors"] / aggregate["requests"]
            yield MetricRow(stage, "error_rate", VALUE, rate)
        for latency in LATENCIES:
            if latency in aggregate:
                yield from _latency_rows(stage, f"{latency}_seconds", aggregate[latency])
    for quality in analysis["quality"]["stages"]:
        stage = str(quality["stage_index"])
        for flag in STAGE_FLAGS:
            yield MetricRow(stage, flag, VALUE, _flag(quality[flag]))


def _latency_rows(stage: str, metric: str, summary: Mapping[str, Any]) -> Iterator[MetricRow]:
    for stat in ("mean", "max"):
        if f"{stat}_seconds" in summary:
            yield MetricRow(stage, metric, stat, float(summary[f"{stat}_seconds"]))
    for percentile in PERCENTILES:
        interval = summary[f"{percentile}_seconds"]
        if "value" not in interval:
            continue
        yield MetricRow(stage, metric, percentile, float(interval["value"]))
        yield MetricRow(stage, metric, f"{percentile}_ci_low", float(interval["ci_low"]))
        yield MetricRow(stage, metric, f"{percentile}_ci_high", float(interval["ci_high"]))


def _run_rows(analysis: Mapping[str, Any]) -> Iterator[MetricRow]:
    quality = analysis["quality"]
    excluded = _flag(quality["is_excluded_from_comparison"])
    yield MetricRow(RUN_STAGE, "is_excluded_from_comparison", VALUE, excluded)
    capacity = analysis["capacity"]
    if slo_max := capacity.get("slo_max"):
        yield MetricRow(RUN_STAGE, "slo_max_rps", VALUE, float(slo_max["rps"]))
        interpolated = _flag(slo_max["is_interpolated"])
        yield MetricRow(RUN_STAGE, "slo_max_is_interpolated", VALUE, interpolated)
    if knee := capacity.get("knee"):
        yield MetricRow(RUN_STAGE, "knee_stage_index", VALUE, float(knee["stage_index"]))
    if per_replica := capacity.get("per_replica"):
        for name in ("ready_replicas", "rps_per_replica", "total_gpus", "rps_per_gpu"):
            if name in per_replica:
                yield MetricRow(RUN_STAGE, name, VALUE, float(per_replica[name]))


def _flag(value: bool) -> float:
    return 1.0 if value else 0.0
