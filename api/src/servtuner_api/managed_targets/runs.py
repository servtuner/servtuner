"""What a managed target's runs and sessions derive from it: the RunSpec target block, labels,
credentials, and the objectives they can rank by."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from servtuner_api.managed_targets.spec import (
    MANAGED_TARGET_LABEL,
    MANAGED_TARGET_RUN_LABEL,
    SERVING_PORT,
    HostTargetSpec,
    ManagedTargetSpec,
    SecretKeyRef,
    TlsTransport,
)
from servtuner_api.resources.targets import CredentialStatus

# What a tuning session may rank by, by what the target's runs will know. Token
# cost needs a router in front of priced providers, never a vLLM. Infrastructure cost needs the
# runs to carry prices (a priced host). A host's runs know their replicas and GPUs
# only from their host's declared hardware; no host run has a sized CPU request.
KUBERNETES_OBJECTIVES = (
    "slo_rps_per_gpu",  # servtuner propose's default
    "slo_max_rps",
    "slo_rps_per_replica",
    "slo_rps_per_core",
    "tokens_per_s_per_gpu",
    "p95_ttft_at_target_rps",
)


HOST_OBJECTIVES = ("slo_max_rps", "p95_ttft_at_target_rps")


HOST_HARDWARE_OBJECTIVES = ("slo_rps_per_gpu", "slo_rps_per_replica", "tokens_per_s_per_gpu")


INFRA_COST_OBJECTIVES = ("infra_cost_per_1k_requests", "infra_cost_per_1m_output_tokens")


def session_objectives(
    spec: ManagedTargetSpec | HostTargetSpec,
    hardware: Mapping[str, object] | None,
    is_priced: bool,
) -> list[str]:
    """The objectives a session on this target can rank by, the default first."""
    if isinstance(spec, HostTargetSpec):
        # A host keeps slo_max_rps as its default, so its sessions rank as they always have.
        objectives = list(HOST_OBJECTIVES) + (list(HOST_HARDWARE_OBJECTIVES) if hardware else [])
    elif spec.gpus == 0:
        # A CPU vLLM: nothing to divide by GPUs, so it ranks by its SLO-max like a bare host.
        per_gpu = {"slo_rps_per_gpu", "tokens_per_s_per_gpu"}
        objectives = ["slo_max_rps"] + [
            name for name in KUBERNETES_OBJECTIVES if name not in per_gpu | {"slo_max_rps"}
        ]
    else:
        objectives = list(KUBERNETES_OBJECTIVES)
    return objectives + (list(INFRA_COST_OBJECTIVES) if is_priced else [])


def ca_secret_name(name: str) -> str:
    """The runs-namespace Secret holding a tls target's ca.crt.

    bench Jobs run in the runs namespace and cannot mount a Secret from the
    managed one, so the deployer copies ca.crt here.
    """
    return f"servtuner-managed-{name}-ca"


def run_target(
    name: str,
    namespace: str,
    spec: ManagedTargetSpec | HostTargetSpec,
    hardware: Mapping[str, object] | None = None,
) -> dict[str, Any]:
    """The RunSpec target block of a managed target's runs; a host target's carries its host's
    hardware (``hardware``) when the host's is known.

    The mode is written out, never left to a default.
    """
    if isinstance(spec, HostTargetSpec):
        # Off-cluster: Prometheus finds it by its scrape job's labels (RunSpec target.metrics).
        metrics: dict[str, Any] = {
            "job": spec.metrics.job,
            "namespace": namespace,
            "pod": spec.metrics.pod,
        }
        if spec.metrics.node:
            metrics["node"] = spec.metrics.node
        target: dict[str, Any] = {
            "type": "vllm",
            "endpoint": spec.endpoint,
            "model": spec.served_model_name or spec.model,
            "metrics": metrics,
            "tls": {"enabled": False},  # endpoint is http:// (plain by default)
        }
        if spec.auth:
            target["auth"] = {"secretRef": {"name": spec.auth.name, "key": spec.auth.key}}
        if hardware is not None:
            target["hardware"] = dict(hardware)
        return target
    is_tls = isinstance(spec.transport, TlsTransport)
    scheme = "https" if is_tls else "http"
    tls: dict[str, Any] = (
        {"enabled": True, "caSecretRef": {"name": ca_secret_name(name)}}
        if is_tls
        else {"enabled": False}
    )
    return {
        "type": "vllm",
        "endpoint": f"{scheme}://{name}.{namespace}.svc:{SERVING_PORT}/v1",
        "model": spec.served_model_name or spec.model,
        "workload": {"namespace": namespace, "selector": {MANAGED_TARGET_LABEL: name}},
        "tls": tls,
    }


# A host target's write-only credentials (as saved targets keep keys), by
# slot, and the spec field each one fills.
HOST_CREDENTIAL_FIELDS = {"launcher_token": "launcher_token", "api_key": "auth"}


def managed_credential_name(target_id: str, slot: str) -> str:
    """The Secret servtuner owns for a pasted credential (the secret store's namespace: runs)."""
    return f"servtuner-managed-{target_id.lower()}-{slot.replace('_', '-')}"


def credential_statuses(
    target_id: str, spec: ManagedTargetSpec | HostTargetSpec
) -> dict[str, CredentialStatus]:
    """Where each host credential lives, never its value: servtuner's Secret or one the user
    names."""
    if not isinstance(spec, HostTargetSpec):
        return {}
    statuses: dict[str, CredentialStatus] = {}
    for slot, field in HOST_CREDENTIAL_FIELDS.items():
        ref: SecretKeyRef | None = getattr(spec, field)
        if ref is None:
            statuses[slot] = CredentialStatus(is_set=False)
            continue
        is_managed = ref.name == managed_credential_name(target_id, slot)
        statuses[slot] = CredentialStatus(
            is_set=True, source="managed" if is_managed else "external", secret_name=ref.name
        )
    return statuses


def run_labels(target_id: str) -> dict[str, str]:
    """The labels every run of a managed target carries."""
    return {MANAGED_TARGET_RUN_LABEL: target_id.lower()}
