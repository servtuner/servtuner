"""What a finished session carries: the recommended command line and the Pareto set."""

from __future__ import annotations

import logging
from typing import Any

from servtuner_api.database import TuningSessionRow
from servtuner_api.managed_targets.spec import parse_spec
from servtuner_api.recommendation import recommend
from servtuner_api.resources.experiments import ExperimentAnalyze
from servtuner_api.tuning.ports import ExperimentAnalyzer, ManagedOps

logger = logging.getLogger(__name__)


def with_pareto(
    row: TuningSessionRow, outcome: dict[str, Any], experiments: ExperimentAnalyzer
) -> dict[str, Any]:
    """servtuner compare over the session's runs (its experiment, kept like the experiment
    page's): the Pareto set of the objective against p95 TTFT. The recommendation
    stands without it, so a comparison that cannot run is only noted."""
    objective = (row.request.get("space") or {}).get("objective")
    try:
        analysis = experiments.analyze(row.experiment, ExperimentAnalyze(objective=objective))
    except Exception as error:  # noqa: BLE001 - any failure leaves the outcome as it was
        logger.warning("tuning session %s: no comparison: %s", row.id, error)
        return {**outcome, "pareto_error": str(error)}
    if analysis is None:
        return outcome
    # JSON throughout: the outcome is stored as a JSON column.
    compared = analysis.model_dump(mode="json", exclude_none=True)
    comparison, objective_name = compared["comparison"], compared["objective"]
    pareto = [
        {
            "knobs": configuration["knobs"],
            "run_ids": configuration["run_ids"],
            "objective_value": next(
                (
                    estimate["estimate"].get("value")
                    for estimate in configuration.get("objectives", [])
                    if estimate["objective"] == objective_name
                ),
                None,
            ),
            # p95 TTFT at the comparison's reference stage, the Pareto set's other axis.
            "ttft_p95_seconds": (configuration.get("reference_ttft_p95_seconds") or {}).get(
                "value"
            ),
        }
        for configuration in comparison.get("configurations", [])
        if configuration.get("is_pareto")
    ]
    return {
        **outcome,
        "pareto": pareto,
        "comparison": {"analysis_id": analysis.id, "objective": objective_name},
    }


def with_recommendation(
    row: TuningSessionRow, outcome: dict[str, Any], managed: ManagedOps
) -> dict[str, Any]:
    """Attach the recommended command line: the winner, else the unconfirmed leader."""
    best = outcome.get("winner") or outcome.get("leader")
    if not best:
        return outcome
    try:
        target = managed.get(row.managed_target_id)
        recommendation = recommend(
            parse_spec(target.spec),
            row.request,
            best,
            confirmed="winner" in outcome,
            knob_args=managed.knob_args(row.managed_target_id, best["knobs"]),
            history=row.history,
            boot_results=row.boot_results,
            notes=outcome.get("notes", []),
        )
    except Exception as error:  # the session's result stands without its command line
        logger.exception("tuning session %s: no recommendation", row.id)
        return {**outcome, "recommendation_error": str(error)}
    return {**outcome, "recommendation": recommendation}
