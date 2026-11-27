"""Stand-ins for the deployer's Kubernetes calls and check-target."""

from collections.abc import Mapping
from typing import Any

from servtuner_api.deployer.base import ObjectRef
from servtuner_api.resources.targets import TargetCheckView

NAME, NAMESPACE = "qwen", "servtuner-tune-qwen"


def check_view(
    status: str, failed: str | None = None, warned: str | None = None
) -> TargetCheckView:
    checks = [{"name": "connect", "status": "ok", "message": "ok", "duration_seconds": 0.01}]
    for name, step_status in ((failed, "failed"), (warned, "warning")):
        if name:
            checks.append(
                {"name": name, "status": step_status, "message": "no", "duration_seconds": 0.01}
            )
    return TargetCheckView.model_validate(
        {
            "result": {
                "status": status,
                "checked_at": "2026-11-23T10:00:00Z",
                "target_type": "vllm",
                "endpoint": f"http://{NAME}.{NAMESPACE}.svc:8000/v1",
                "checks": checks,
            },
            "checked_from": "the test",
        }
    )


class FakeChecker:
    def __init__(self, view: TargetCheckView) -> None:
        self.view = view
        self.requests: list[dict[str, Any]] = []

    def check(self, request: dict[str, Any], one_time: tuple[str, str] | None = None) -> Any:
        self.requests.append(request)
        return self.view

    def boot_probe(self, request: dict[str, Any]) -> dict[str, Any]:
        """As servtuner boot-probe answers a target with 8192 blocks of 16 tokens."""
        self.requests.append(request)
        c_kv = 8192 * 16 / request["tokens_per_request"]
        return {
            "knobs": request["knobs"],
            "outcome": "feasible",
            "num_gpu_blocks": 8192,
            "c_kv": c_kv,
        }


class FakeCluster:
    def __init__(self) -> None:
        self.objects: dict[ObjectRef, dict[str, Any]] = {}
        self.secrets: dict[tuple[str, str], dict[str, str]] = {
            (NAMESPACE, "hf-token"): {"token": "hf_x"}
        }
        self.pod_list: list[dict[str, Any]] = []
        self.logs: dict[str, str] = {}
        self.applied: list[ObjectRef] = []
        self.deleted: list[ObjectRef] = []
        self.scaled: list[tuple[str, str, int]] = []

    def get(self, ref: ObjectRef) -> dict[str, Any] | None:
        return self.objects.get(ref)

    def apply(self, manifest: dict[str, Any]) -> None:
        ref = ObjectRef.of(manifest)
        self.applied.append(ref)
        self.objects[ref] = manifest

    def delete(self, ref: ObjectRef) -> None:
        self.deleted.append(ref)
        self.objects.pop(ref, None)

    def scale(self, namespace: str, name: str, replicas: int) -> None:
        self.scaled.append((namespace, name, replicas))

    def pods(self, namespace: str, labels: Mapping[str, str]) -> list[dict[str, Any]]:
        return self.pod_list

    def log(self, namespace: str, pod: str, container: str, tail_lines: int) -> str:
        return self.logs.get(pod, "")

    def secret_value(self, namespace: str, name: str, key: str) -> str | None:
        return self.secrets.get((namespace, name), {}).get(key)
