"""The RunSpec of a tuning session's run, and the token count its boot probes size for."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from servtuner_api.managed_targets.runs import run_labels
from servtuner_api.tuning.models import SESSION_LABEL, SessionCreate


def tokens_per_request(preset: Mapping[str, Any]) -> float:
    """The preset's planned prompt + output tokens per request, for c_kv (boot probes)."""
    dataset = preset["workload"]["dataset"]
    return float(dataset["input_tokens"]["mean"]) + float(dataset["output_tokens"]["mean"])


def build_run_spec(
    session_id: str,
    request: SessionCreate,
    experiment: str,
    run_target: Mapping[str, Any],
    proposed: Mapping[str, Any],
    repeat: int,
    prometheus: Mapping[str, Any] | None,
    analysis: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """The RunSpec of one repeat of a proposed run: the session's workload, SLOs and target,
    the proposal's knobs and ladder, and the labels servtuner propose classifies runs by."""
    config_id = str(proposed["config_id"])
    spec: dict[str, Any] = {
        "apiVersion": "servtuner.dev/v1",
        "kind": "RunSpec",
        "name": f"tune-{session_id[-6:].lower()}-{config_id}-{repeat}",
        "experiment": experiment,
        "labels": {
            **proposed.get("labels", {}),
            **run_labels(request.managed_target_id),
            SESSION_LABEL: session_id.lower(),
        },
        "knobs": dict(proposed["knobs"]),
        "target": dict(run_target),
        "engine": {"type": "loadgen"},
        "workload": {"preset": request.preset, "streaming": request.streaming},
        "load": {
            "pacing": "poisson",
            "warmup": request.warmup,
            "stages": [
                {"rate": rate, "duration": request.stage_duration}
                for rate in proposed["ladder"]["rps"]
            ],
        },
        "slos": dict(request.slos),
    }
    if prometheus is not None:
        spec["prometheus"] = dict(prometheus)
    if analysis is not None:
        spec["analysis"] = dict(analysis)  # a host's prices
    return spec
