"""The recommended vLLM command line of a tuning session.

A pure function of what the session recorded: the best configuration's knobs
and evidence, the managed target's spec, the history and the boot results.
The flags come from bench's knob table (``servtuner vllm-args``), like the deploys.
"""

from __future__ import annotations

import shlex
from collections.abc import Mapping
from typing import Any

from servtuner_api.managed_targets.spec import HostTargetSpec, ManagedTargetSpec

# The container name the deployment patch addresses; the user renames it to theirs.
PATCH_CONTAINER = "vllm"


# How the user starts each server: the command before its args.
SERVER_COMMANDS = {"vllm": "vllm serve", "sglang": "python3 -m sglang.launch_server"}


def server_of(spec: ManagedTargetSpec | HostTargetSpec) -> str:
    """The model server a target runs; a host target runs vLLM."""
    return spec.server if isinstance(spec, ManagedTargetSpec) else "vllm"


def user_args(spec: ManagedTargetSpec | HostTargetSpec, knob_args: list[str]) -> list[str]:
    """The args a user's own server needs: the model, the flags never tuned, then the tuned
    ones. servtuner's deploy-only flags (host, port, cache path, certificates, SGLang's
    metrics switch) are left out: the user's Deployment has its own."""
    args = [f"--model-path={spec.model}"] if server_of(spec) == "sglang" else [spec.model]
    if spec.served_model_name:
        args.append(f"--served-model-name={spec.served_model_name}")
    return args + spec.base_flags + knob_args


def measured_for(
    spec: ManagedTargetSpec | HostTargetSpec, request: Mapping[str, Any]
) -> dict[str, Any]:
    """Results hold only for what they were measured on."""
    where: dict[str, Any]
    if isinstance(spec, HostTargetSpec):
        # A host's hardware is not in the spec; its runs' bundles record what bench captured.
        where = {"host_endpoint": spec.endpoint}
    else:
        where = {"image": spec.image, "gpus": spec.gpus, "node_selector": dict(spec.node_selector)}
    return {
        "model": spec.model,
        **where,
        "preset": request["preset"],
        "slos": dict(request["slos"]),
    }


def recommend(
    spec: ManagedTargetSpec | HostTargetSpec,
    request: Mapping[str, Any],
    best: Mapping[str, Any],
    *,
    confirmed: bool,
    knob_args: list[str],
    history: list[dict[str, Any]],
    boot_results: list[dict[str, Any]],
    notes: list[str] | None = None,
) -> dict[str, Any]:
    """The recommendation: a literal command line, the evidence for it, and a patch.

    ``best`` is the confirmed winner, or the leader so far when the budget ran out
    (``confirmed`` False: the leader never had its confirmation repeats).
    """
    args = user_args(spec, knob_args)
    config_id = str(best["config_id"])
    evidence = {
        key: best[key]
        for key in (
            "config_id",
            "objective_value",
            "slo_max_rps",
            "is_significant",
            "confidence",
            "run_ids",
        )
        if key in best
    }
    return {
        "command": SERVER_COMMANDS[server_of(spec)]
        + " "
        + " ".join(shlex.quote(arg) for arg in args),
        "args": args,
        "knobs": dict(best["knobs"]),
        "confirmed": confirmed,
        "evidence": evidence,
        # Results hold only for what they were measured on.
        "measured_for": measured_for(spec, request),
        "steps": [
            {
                key: entry[key]
                for key in ("action", "config_id", "knobs", "rationale", "run_ids")
                if key in entry
            }
            for entry in history
        ],
        "winner_steps": [entry for entry in history if entry.get("config_id") == config_id],
        "infeasible": [result for result in boot_results if result.get("outcome") == "infeasible"],
        # servtuner propose's findings, such as gpu_memory_utilization values that booted with the
        # same KV cache and so made no difference on this host.
        "notes": list(notes or []),
        # How the winner's KV cache was sized, when vLLM logged it (vLLM 0.34+).
        "kv_sizing": _kv_sizing_of(best["knobs"], boot_results),
        # A strategic merge patch replaces the whole args list, so it carries all of them.
        "deployment_patch": {
            "spec": {
                "template": {"spec": {"containers": [{"name": PATCH_CONTAINER, "args": args}]}}
            }
        },
    }


def _kv_sizing_of(
    knobs: Mapping[str, Any], boot_results: list[dict[str, Any]]
) -> dict[str, Any] | None:
    """The kv_sizing of the feasible boot probe whose knobs the winner's contain."""
    for result in boot_results:
        probed = result.get("knobs", {})
        if (
            result.get("outcome") == "feasible"
            and "kv_sizing" in result
            and all(knobs.get(name) == value for name, value in probed.items())
        ):
            return dict(result["kv_sizing"])
    return None
