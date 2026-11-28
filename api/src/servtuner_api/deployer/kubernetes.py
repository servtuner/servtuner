"""The Deployer on a cluster: render, check ownership and Secrets, apply, poll; and the
lightkube client it uses."""

from __future__ import annotations

import base64
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any, override

from servtuner_api.deployer.base import (
    HTTP_NOT_FOUND,
    AnySpec,
    BootProbes,
    Checker,
    DeployInputError,
    ManagedCluster,
    ObjectRef,
    OwnershipError,
    Readiness,
    VllmLog,
    kubernetes_spec,
)
from servtuner_api.deployer.boot import LOG_TAIL_LINES, SIZING_LOG_TAIL_LINES, classify_boot_failure
from servtuner_api.deployer.manifests import (
    CONTAINER,
    MANAGED_BY,
    MANAGED_BY_LABEL,
    Monitor,
    is_owned,
    render_manifests,
    serving_args,
    tls_secret_name,
)
from servtuner_api.kubernetes_decoders import prepare_decoders
from servtuner_api.managed_targets.runs import ca_secret_name, run_target
from servtuner_api.managed_targets.spec import (
    MANAGED_TARGET_LABEL,
    ManagedTargetSpec,
    ObjectStoreModel,
    TlsTransport,
)
from servtuner_api.secret_store import SecretStore

# Waiting reasons that never clear by waiting: the deploy itself is wrong.
DEFECT_WAITING_REASONS = frozenset(
    {"ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError"}
)


@dataclass
class Deployer(BootProbes):
    cluster: ManagedCluster
    render_knobs: Callable[[Mapping[str, Any], str], list[str]]
    checker: Checker
    store: SecretStore | None
    prometheus: dict[str, Any] | None = None
    monitor: Monitor = "podmonitor"
    image_pull_secrets: tuple[str, ...] = ()
    offline: bool = False

    def deploy(
        self, name: str, namespace: str, any_spec: AnySpec, knobs: Mapping[str, Any]
    ) -> list[str]:
        """Apply the target with these knobs; returns the container args it rendered."""
        spec = kubernetes_spec(any_spec)
        if self.monitor == "scrape-annotations" and tls_secret_name(name, spec) is not None:
            # The community chart's servtuner-targets job has no CA for a private certificate, so
            # the scrape would fail and the deploy would only time out.
            raise DeployInputError(
                [
                    "transport with TLS needs the podmonitor monitor (the Prometheus Operator): "
                    "the "
                    "community chart's servtuner-targets job cannot verify the target's certificate"
                ]
            )
        args = serving_args(name, spec, self.render_knobs(knobs, spec.server))
        manifests = render_manifests(
            name, namespace, spec, args, self.monitor, self.image_pull_secrets, self.offline
        )
        self._check_ownership(name, manifests)
        self._check_secrets(name, namespace, spec)
        for manifest in manifests:
            self.cluster.apply(manifest)
        return args

    @override
    def poll(
        self, target_id: str, name: str, namespace: str, any_spec: AnySpec, args: list[str]
    ) -> Readiness:
        """One readiness step: boot failures first, then rollout, args, CA copy and check-target."""
        spec = kubernetes_spec(any_spec)
        deployment = self.cluster.get(ObjectRef("apps/v1", "Deployment", namespace, name))
        if deployment is None:
            return Readiness("failed", "the Deployment does not exist: deploy first")
        pods = self.cluster.pods(namespace, {MANAGED_TARGET_LABEL: name})
        for pod in pods:
            outcome = self._boot_outcome(namespace, pod, spec.server)
            if outcome is not None:
                return outcome
        status = deployment.get("status") or {}
        generation = (deployment.get("metadata") or {}).get("generation", 0)
        rolled_out = (
            status.get("observedGeneration", 0) >= generation
            and status.get("updatedReplicas") == 1
            and status.get("readyReplicas") == 1
        )
        if not rolled_out:
            return Readiness("pending", _pending_reason(pods))
        running = [pod for pod in pods if _is_ready(pod)]
        if len(running) != 1:
            return Readiness("pending", "waiting for the old pod to go")
        captured = _container_args(running[0])
        if captured != args:
            return Readiness(
                "failed", f"the pod runs other args than rendered: {captured} != {args}"
            )
        if (problem := self._copy_ca(target_id, name, namespace, spec)) is not None:
            return Readiness("failed", problem)
        request: dict[str, Any] = {"target": run_target(name, namespace, spec)}
        if self.prometheus is not None:
            request["prometheus"] = self.prometheus
        view = self.checker.check(request)
        # Only a failed check holds the run back. A warning (no GPU metrics on a node without DCGM,
        # a skipped step) is reported and the run still starts: it would never clear by waiting.
        failed = [step.name.value for step in view.result.checks if step.status.value == "failed"]
        if failed:
            return Readiness(
                "pending", f"check-target not passing yet: {', '.join(failed)}", check=view
            )
        warned = [step.name.value for step in view.result.checks if step.status.value == "warning"]
        reason = "rolled out, args as rendered, check-target ok"
        if warned:
            reason += f" (warnings: {', '.join(warned)})"
        return Readiness("ready", reason, check=view)

    def scale_to_zero(self, name: str, namespace: str, spec: AnySpec | None = None) -> None:
        self._require_owned(ObjectRef("apps/v1", "Deployment", namespace, name), name)
        self.cluster.scale(namespace, name, 0)

    def teardown(
        self, name: str, namespace: str, any_spec: AnySpec, *, keep_model_cache: bool = True
    ) -> None:
        """Delete what servtuner deployed for the target; the model cache stays unless asked."""
        spec = kubernetes_spec(any_spec)
        for manifest in reversed(render_manifests(name, namespace, spec, [], "podmonitor")):
            ref = ObjectRef.of(manifest)
            if ref.kind == "PersistentVolumeClaim" and keep_model_cache:
                continue
            existing = self.cluster.get(ref)
            if existing is not None and is_owned(existing, name):
                self.cluster.delete(ref)
        if self.store is not None and tls_secret_name(name, spec) is not None:
            self.store.delete(ca_secret_name(name))

    def _check_ownership(self, name: str, manifests: list[dict[str, Any]]) -> None:
        for manifest in manifests:
            self._require_owned(ObjectRef.of(manifest), name)

    def _require_owned(self, ref: ObjectRef, name: str) -> None:
        existing = self.cluster.get(ref)
        if existing is not None and not is_owned(existing, name):
            raise OwnershipError(
                f"{ref.kind} {ref.namespace}/{ref.name} exists without servtuner's labels "
                f"({MANAGED_BY_LABEL}={MANAGED_BY}, {MANAGED_TARGET_LABEL}={name}): "
                "servtuner will not change it"
            )

    def _check_secrets(self, name: str, namespace: str, spec: ManagedTargetSpec) -> None:
        problems: list[str] = []
        if (
            spec.hf_token
            and self.cluster.secret_value(namespace, spec.hf_token.name, spec.hf_token.key) is None
        ):
            problems.append(
                f"Secret {namespace}/{spec.hf_token.name} has no key {spec.hf_token.key} (hf_token)"
            )
        source = spec.model_source
        if isinstance(source, ObjectStoreModel) and source.credentials:
            secret = source.credentials
            for key in (secret.access_key_id_key, secret.secret_access_key_key):
                if self.cluster.secret_value(namespace, secret.name, key) is None:
                    problems.append(
                        f"Secret {namespace}/{secret.name} has no key {key}"
                        " (model_source.credentials)"
                    )
        transport = spec.transport
        if isinstance(transport, TlsTransport) and transport.secret:
            for key in ("tls.crt", "tls.key", "ca.crt"):
                if self.cluster.secret_value(namespace, transport.secret, key) is None:
                    problems.append(
                        f"Secret {namespace}/{transport.secret} has no {key} (transport.secret)"
                    )
        if problems:
            raise DeployInputError(problems)

    def _copy_ca(
        self, target_id: str, name: str, namespace: str, spec: ManagedTargetSpec
    ) -> str | None:
        """Put the server's CA where bench Jobs can mount it (the runs namespace)."""
        secret = tls_secret_name(name, spec)
        if secret is None:
            return None
        ca = self.cluster.secret_value(namespace, secret, "ca.crt")
        if ca is None:
            return f"Secret {namespace}/{secret} has no ca.crt: bench could not verify the server"
        if self.store is None:
            return "no secret store: the CA cannot reach the runs namespace"
        self.store.put(ca_secret_name(name), {"ca.crt": ca}, target_id)
        return None

    @override
    def boot_log(self, name: str, namespace: str, any_spec: AnySpec) -> str:
        running = [
            pod
            for pod in self.cluster.pods(namespace, {MANAGED_TARGET_LABEL: name})
            if _is_ready(pod)
        ]
        if len(running) != 1:
            return ""
        return self.cluster.log(
            namespace, running[0]["metadata"]["name"], CONTAINER, SIZING_LOG_TAIL_LINES
        )

    @override
    def recent_log(self, name: str, namespace: str, any_spec: AnySpec, tail_lines: int) -> VllmLog:
        pods = self.cluster.pods(namespace, {MANAGED_TARGET_LABEL: name})
        if not pods:
            return VllmLog(source="no pod: not deployed, or scaled to zero", text="")
        # RFC 3339 timestamps in UTC sort as strings.
        newest = max(pods, key=lambda pod: pod["metadata"].get("creationTimestamp") or "")
        pod_name = newest["metadata"]["name"]
        status = _container_status(newest) or {}
        state = status.get("state") or {}
        has_exited_before = "terminated" in (status.get("lastState") or {})  # CrashLoopBackOff
        if not ({"running", "terminated"} & state.keys() or has_exited_before):
            # kubelet has no log for a container that never started (pulling, unschedulable).
            waiting = (state.get("waiting") or {}).get("reason") or "not started"
            return VllmLog(source=f"pod {pod_name} ({waiting})", text="")
        text = self.cluster.log(namespace, pod_name, CONTAINER, tail_lines)
        return VllmLog(source=f"pod {pod_name}", text=text)

    def _boot_outcome(self, namespace: str, pod: dict[str, Any], server: str) -> Readiness | None:
        status = _container_status(pod)
        if status is None:
            return None
        state = status.get("state") or {}
        waiting = state.get("waiting") or {}
        if waiting.get("reason") in DEFECT_WAITING_REASONS:
            return Readiness("failed", f"{waiting['reason']}: {waiting.get('message', '')}".strip())
        # Classify only while no newer instance runs, so the log read is the one that exited.
        # A restarted instance that is running is still loading: the next poll sees how it ends.
        if "running" in state:
            return None
        terminated = state.get("terminated") or (status.get("lastState") or {}).get("terminated")
        if not terminated or terminated.get("exitCode", 0) == 0:
            return None
        log = self.cluster.log(namespace, pod["metadata"]["name"], CONTAINER, LOG_TAIL_LINES)
        failure = classify_boot_failure(
            log, terminated.get("exitCode"), terminated.get("reason"), server
        )
        state_name = "infeasible" if failure.kind == "infeasible" else "failed"
        return Readiness(state_name, f"{failure.rule}: {failure.message}", failure=failure)


def _container_status(pod: Mapping[str, Any]) -> dict[str, Any] | None:
    for status in (pod.get("status") or {}).get("containerStatuses") or []:
        if status.get("name") == CONTAINER:
            return dict(status)
    return None


def _container_args(pod: Mapping[str, Any]) -> list[str]:
    for container in pod["spec"]["containers"]:
        if container["name"] == CONTAINER:
            return list(container.get("args") or [])
    return []


def _is_ready(pod: Mapping[str, Any]) -> bool:
    if (pod.get("metadata") or {}).get("deletionTimestamp"):
        return False
    status = _container_status(pod)
    return bool(status and status.get("ready"))


def _pending_reason(pods: list[dict[str, Any]]) -> str:
    for pod in pods:
        for condition in (pod.get("status") or {}).get("conditions") or []:
            if condition.get("type") == "PodScheduled" and condition.get("status") == "False":
                return f"not scheduled: {condition.get('message', condition.get('reason', ''))}"
    return "rolling out: the model is loading" if pods else "waiting for a pod"


class LightkubeManagedCluster:
    """``ManagedCluster`` over lightkube with the pod's in-cluster config.

    Manifests go through ``codecs.from_dict``; cert-manager's Certificate and the
    Prometheus Operator's PodMonitor are registered as generic resources.
    """

    def __init__(self, client: Any | None = None) -> None:
        prepare_decoders()
        from lightkube import Client
        from lightkube.generic_resource import create_namespaced_resource

        create_namespaced_resource("cert-manager.io", "v1", "Certificate", "certificates")
        create_namespaced_resource("monitoring.coreos.com", "v1", "PodMonitor", "podmonitors")
        self._client = client or Client(field_manager=MANAGED_BY)

    def _resource(self, ref: ObjectRef) -> Any:
        from lightkube.codecs import resource_registry

        return resource_registry.load(ref.api_version, ref.kind)

    def get(self, ref: ObjectRef) -> dict[str, Any] | None:
        from lightkube import ApiError

        try:
            found = self._client.get(self._resource(ref), ref.name, namespace=ref.namespace)
        except ApiError as error:
            if error.status.code == HTTP_NOT_FOUND:
                return None
            raise
        return dict(found.to_dict())

    def apply(self, manifest: dict[str, Any]) -> None:
        from lightkube import codecs

        self._client.apply(codecs.from_dict(manifest), field_manager=MANAGED_BY, force=True)

    def delete(self, ref: ObjectRef) -> None:
        from lightkube import ApiError

        try:
            self._client.delete(self._resource(ref), ref.name, namespace=ref.namespace)
        except ApiError as error:
            if error.status.code != HTTP_NOT_FOUND:
                raise

    def scale(self, namespace: str, name: str, replicas: int) -> None:
        from lightkube.resources.apps_v1 import Deployment
        from lightkube.types import PatchType

        self._client.patch(
            Deployment,
            name,
            {"spec": {"replicas": replicas}},
            namespace=namespace,
            patch_type=PatchType.MERGE,
        )

    def pods(self, namespace: str, labels: Mapping[str, str]) -> list[dict[str, Any]]:
        from lightkube.resources.core_v1 import Pod

        # lightkube's selector values are wider than str, and dict is invariant.
        selector: dict[str, Any] = dict(labels)
        return [
            dict(pod.to_dict())
            for pod in self._client.list(Pod, namespace=namespace, labels=selector)
        ]

    def log(self, namespace: str, pod: str, container: str, tail_lines: int) -> str:
        return "".join(
            self._client.log(
                pod, namespace=namespace, container=container, tail_lines=tail_lines, newlines=True
            )
        )

    def secret_value(self, namespace: str, name: str, key: str) -> str | None:
        from lightkube import ApiError
        from lightkube.resources.core_v1 import Secret

        try:
            secret = self._client.get(Secret, name, namespace=namespace)
        except ApiError as error:
            if error.status.code == HTTP_NOT_FOUND:
                return None
            raise
        encoded = (secret.data or {}).get(key)
        return None if encoded is None else base64.b64decode(encoded).decode()
