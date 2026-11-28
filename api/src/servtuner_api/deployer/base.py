"""What both deployers share: the cluster and checker protocols, errors, readiness, logs and
boot probes."""

from __future__ import annotations

import json
import subprocess
import tempfile
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Literal, Protocol

from servtuner_api.cli_contract import error_message
from servtuner_api.deployer.boot import BootFailure, parse_kv_sizing, user_facing
from servtuner_api.managed_targets.runs import run_target
from servtuner_api.managed_targets.spec import HostTargetSpec, ManagedTargetSpec

HTTP_NOT_FOUND = 404


HTTP_OK = 200


HTTP_BAD_REQUEST = 400


VLLM_ARGS_TIMEOUT_SECONDS = 15.0


@dataclass(frozen=True)
class ObjectRef:
    api_version: str
    kind: str
    namespace: str
    name: str

    @classmethod
    def of(cls, manifest: Mapping[str, Any]) -> ObjectRef:
        metadata = manifest["metadata"]
        return cls(
            manifest["apiVersion"], manifest["kind"], metadata["namespace"], metadata["name"]
        )


class ManagedCluster(Protocol):
    """The Kubernetes calls the deployer makes, in the managed namespaces."""

    def get(self, ref: ObjectRef) -> dict[str, Any] | None: ...

    def apply(self, manifest: dict[str, Any]) -> None:
        """Server-side apply, field manager servtuner."""
        ...

    def delete(self, ref: ObjectRef) -> None:
        """Delete; a missing object is not an error."""
        ...

    def scale(self, namespace: str, name: str, replicas: int) -> None: ...

    def pods(self, namespace: str, labels: Mapping[str, str]) -> list[dict[str, Any]]: ...

    def log(self, namespace: str, pod: str, container: str, tail_lines: int) -> str:
        """The container's log. For a container that is terminated or in CrashLoopBackOff, kubelet
        serves the instance that exited (no newer one exists yet)."""
        ...

    def secret_value(self, namespace: str, name: str, key: str) -> str | None:
        """A Secret key's decoded value, or None when the Secret or key is missing. Never logged."""
        ...


class Checker(Protocol):
    """servtuner check-target and boot-probe with the request's Secrets (targets.TargetChecker)."""

    def check(self, request: dict[str, Any], one_time: tuple[str, str] | None = None) -> Any: ...

    def boot_probe(self, request: dict[str, Any]) -> dict[str, Any]: ...


class OwnershipError(RuntimeError):
    """An object servtuner would write exists without servtuner's labels: it belongs to someone
    else."""


class DeployInputError(ValueError):
    """The deploy cannot start: a missing Secret or key, or knobs bench cannot render."""

    def __init__(self, problems: list[str]) -> None:
        super().__init__("; ".join(problems))
        self.problems = problems


@dataclass(frozen=True)
class Readiness:
    """One poll's answer. ``state`` is pending (keep polling), ready (start the run),
    infeasible (a configuration result: record it and move on) or failed (stop: a defect)."""

    state: Literal["pending", "ready", "infeasible", "failed"]
    reason: str
    failure: BootFailure | None = None
    check: Any = None


def bench_server_args(bench_binary: Path) -> Callable[[Mapping[str, Any], str], list[str]]:
    """Render knobs through ``servtuner server-args`` (bench's knob tables, the one source):
    vLLM's, or SGLang's catalog."""

    def render(knobs: Mapping[str, Any], server: str = "vllm") -> list[str]:
        with tempfile.TemporaryDirectory(prefix="servtuner-knobs-") as directory:
            path = Path(directory) / "knobs.json"
            path.write_text(json.dumps(dict(knobs)))
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                [str(bench_binary), "server-args", "--server", server, "--knobs", str(path)],
                capture_output=True,
                text=True,
                timeout=VLLM_ARGS_TIMEOUT_SECONDS,
                check=False,
            )
        if completed.returncode != 0:
            lines = completed.stderr.strip().splitlines() or ["servtuner server-args failed"]
            raise DeployInputError([error_message(line) for line in lines])
        args: list[str] = json.loads(completed.stdout)
        return args

    return render


AnySpec = ManagedTargetSpec | HostTargetSpec


def kubernetes_spec(spec: AnySpec) -> ManagedTargetSpec:
    """The Kubernetes backend's own placement; the service routes by placement, so anything else
    reaching it is a bug."""
    if not isinstance(spec, ManagedTargetSpec):
        raise TypeError(f"the Kubernetes deployer got a {spec.placement} target")
    return spec


def host_spec(spec: AnySpec) -> HostTargetSpec:
    if not isinstance(spec, HostTargetSpec):
        raise TypeError(f"the host deployer got a {spec.placement} target")
    return spec


@dataclass(frozen=True)
class VllmLog:
    """The end of vLLM's log, and where it came from (a pod, or a host's launcher)."""

    source: str
    text: str


class BootProbes:
    """A boot probe on any backend: one poll, and a tuningBootResult once the boot has ended."""

    checker: Checker

    def poll(
        self, target_id: str, name: str, namespace: str, any_spec: AnySpec, args: list[str]
    ) -> Readiness:  # pragma: no cover - each backend implements it
        raise NotImplementedError

    def boot_log(
        self, name: str, namespace: str, any_spec: AnySpec
    ) -> str:  # pragma: no cover - each backend implements it
        """The running vLLM's log from its start, as far as the backend keeps it; "" if none."""
        raise NotImplementedError

    def recent_log(
        self, name: str, namespace: str, any_spec: AnySpec, tail_lines: int
    ) -> VllmLog:  # pragma: no cover - each backend implements it
        """The newest vLLM instance's last lines, running or exited, for a person to read."""
        raise NotImplementedError

    def boot_result(
        self,
        target_id: str,
        name: str,
        namespace: str,
        spec: Any,
        deployment: Mapping[str, Any],
        tokens_per_request: float,
    ) -> tuple[Readiness, dict[str, Any] | None]:
        """One poll of a boot probe, and its tuningBootResult once the boot has ended.

        Ready: servtuner boot-probe reads num_gpu_blocks and c_kv, and vLLM's log says which limit
        sized its KV cache (``kv_sizing``). Infeasible: the failure is the result.
        Pending or failed: no result; the caller polls again or stops.
        """
        knobs = dict(deployment["knobs"])
        readiness = self.poll(target_id, name, namespace, spec, list(deployment["args"]))
        if readiness.state == "ready":
            request = {
                "target": run_target(name, namespace, spec),
                "knobs": knobs,
                "tokens_per_request": tokens_per_request,
            }
            result = self.checker.boot_probe(request)
            if result.get("outcome") == "feasible":
                # vLLM's sizing lines; SGLang's pool comes whole from /server_info.
                sizing = (
                    parse_kv_sizing(self.boot_log(name, namespace, spec))
                    if run_target(name, namespace, spec)["type"] == "vllm"
                    else None
                )
                if sizing is not None:
                    result = {**result, "kv_sizing": sizing}
            return readiness, result
        if readiness.state == "infeasible" and readiness.failure is not None:
            evidence = user_facing((readiness.failure.excerpt[-1:] or [""])[0])
            reason = f"{readiness.reason} ({evidence})" if evidence else readiness.reason
            return readiness, {"knobs": knobs, "outcome": "infeasible", "reason": reason}
        return readiness, None


FLASHINFER_NO_DOWNLOAD = "FLASHINFER_NO_DOWNLOAD"


def offline_env(env: Mapping[str, str], offline: bool) -> dict[str, str]:
    """vLLM's environment on an install with no internet access: FlashInfer reads its
    precompiled kernels from what is installed (the flashinfer-cubin wheel) and never downloads
    one it lacks, so the boot fails at once and says so.
    A target that sets the variable itself keeps its value."""
    if offline and FLASHINFER_NO_DOWNLOAD not in env:
        return {**env, FLASHINFER_NO_DOWNLOAD: "1"}
    return dict(env)
