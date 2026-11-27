"""Launching, watching and cancelling a run's Job (``KubernetesJobRunner``)."""

import json
import logging
import threading
import time
from collections.abc import Callable, Iterator

from lightkube.resources.batch_v1 import Job
from lightkube.resources.core_v1 import Pod

from servtuner_api.kubernetes_runner.cluster import Cluster
from servtuner_api.kubernetes_runner.config import (
    BENCH_CONTAINER,
    COMPLETION_INDEX_ANNOTATION,
    KubernetesRunnerConfig,
    job_name,
)
from servtuner_api.kubernetes_runner.jobs import JobBuilder
from servtuner_api.runner import LaunchRequest, RunHandle, RunnerState, RunnerStatus
from servtuner_api.secret_store import secret_names_in

logger = logging.getLogger(__name__)


# Container waiting reasons that mean "will not start without help".
STUCK_REASONS = frozenset(
    {
        "ErrImagePull",
        "ImagePullBackOff",
        "InvalidImageName",
        "CreateContainerConfigError",
        "CreateContainerError",
    }
)


class LaunchError(RuntimeError):
    """The run cannot be launched as specified; the message says why."""


class KubernetesJobRunner:
    """Launches bench as a Job and reports the Job's outcome."""

    def __init__(
        self,
        config: KubernetesRunnerConfig,
        cluster: Cluster,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._config = config
        self._builder = JobBuilder(config)
        self._cluster = cluster
        self._clock = clock
        self._lock = threading.Lock()
        self._cancelled: set[str] = set()
        self._launched_at: dict[str, float] = {}

    def launch(self, request: LaunchRequest) -> RunHandle:
        spec: object = json.loads(request.spec_raw)  # the service validated it
        self._check_secrets(spec)
        name = job_name(request.run_id)
        owner = self._cluster.create_job(self._builder.job(request.run_id, spec))
        try:
            # Owned by the Job: TTL cleanup and deletion remove them too. The pod
            # waits for its volumes until they exist, a moment later.
            self._cluster.create_config_map(self._builder.spec_config_map(request, owner))
            self._cluster.create_secret(self._builder.token_secret(request, owner))
        except Exception:
            self._cluster.delete_job(name)
            raise
        with self._lock:
            self._launched_at[request.run_id] = self._clock()
        return RunHandle(
            run_id=request.run_id, reference=f"{self._config.cluster.namespace}/{name}"
        )

    def status(self, handle: RunHandle) -> RunnerStatus:
        job = self._cluster.get_job(job_name(handle.run_id))
        with self._lock:
            cancelled = handle.run_id in self._cancelled
        if job is None:
            if cancelled:
                return RunnerStatus(RunnerState.CANCELLED)
            return RunnerStatus(RunnerState.FAILED, message="the bench Job no longer exists")
        if cancelled:
            return RunnerStatus(RunnerState.CANCELLED)
        finished = _finished_condition(job)
        if finished is not None:
            return self._final_status(handle.run_id, finished)
        return self._check_started(handle)

    def cancel(self, handle: RunHandle) -> None:
        with self._lock:
            self._cancelled.add(handle.run_id)
        self._cluster.delete_job(job_name(handle.run_id))

    def logs(self, handle: RunHandle) -> Iterator[str]:
        pods = _leader_first(self._cluster.list_pods(handle.run_id))
        if not pods:
            return
        leader = pods[0]  # the leader's log tells the run's story
        assert leader.metadata is not None and leader.metadata.name is not None
        yield from (line.rstrip("\n") for line in self._cluster.pod_log(leader.metadata.name))

    def _check_secrets(self, spec: object) -> None:
        cluster = self._config.cluster
        needed = secret_names_in(spec) + [
            name for name in (cluster.bench_tls_secret, cluster.ca_secret) if name is not None
        ]
        missing = [name for name in needed if not self._cluster.secret_exists(name)]
        if missing:
            # A pod with a missing Secret volume waits forever instead of failing.
            raise LaunchError(
                f"Secrets missing from namespace {cluster.namespace}: {', '.join(missing)}"
            )

    def _final_status(self, run_id: str, condition: str) -> RunnerStatus:
        exit_code, reason = self._termination(run_id)
        if condition == "Complete":
            return RunnerStatus(RunnerState.SUCCEEDED, exit_code=exit_code)
        detail = reason or "the bench Job failed"
        if exit_code is not None:
            detail = f"{detail} (exit code {exit_code})"
        return RunnerStatus(RunnerState.FAILED, exit_code=exit_code, message=detail)

    def _termination(self, run_id: str) -> tuple[int | None, str | None]:
        """The bench container's exit code and reason (OOMKilled, Error, ...), if a pod is left."""
        # The leader's exit code is the run's (shard 0 of a sharded run).
        for pod in _leader_first(self._cluster.list_pods(run_id)):
            for container in (pod.status.containerStatuses or []) if pod.status else []:
                terminated = container.state.terminated if container.state else None
                if container.name == BENCH_CONTAINER and terminated is not None:
                    return terminated.exitCode, terminated.reason
        return None, None

    def _check_started(self, handle: RunHandle) -> RunnerStatus:
        """Running, unless the pod has failed to start for longer than the start timeout."""
        with self._lock:
            launched_at = self._launched_at.setdefault(handle.run_id, self._clock())
        if self._clock() - launched_at < self._config.cluster.start_timeout_seconds:
            return RunnerStatus(RunnerState.RUNNING)
        reason = self._not_started_reason(handle.run_id)
        if reason is None:
            return RunnerStatus(RunnerState.RUNNING)
        logger.warning("run %s: bench pod did not start: %s", handle.run_id, reason)
        with self._lock:
            self._launched_at.pop(handle.run_id, None)
        self._cluster.delete_job(job_name(handle.run_id))
        return RunnerStatus(RunnerState.FAILED, message=f"the bench pod did not start: {reason}")

    def _not_started_reason(self, run_id: str) -> str | None:
        pods = self._cluster.list_pods(run_id)
        if not pods:
            return "no pod was created"
        for pod in pods:
            status = pod.status
            if status is None or status.phase != "Pending":
                return None
            for condition in status.conditions or []:
                if condition.type == "PodScheduled" and condition.status == "False":
                    return f"{condition.reason}: {condition.message}"
            for container in status.containerStatuses or []:
                waiting = container.state.waiting if container.state else None
                if waiting is not None and waiting.reason in STUCK_REASONS:
                    return f"{waiting.reason}: {waiting.message}"
        return None


def _finished_condition(job: Job) -> str | None:
    """``Complete`` or ``Failed`` once the Job controller has decided; else None."""
    conditions = job.status.conditions if job.status else None
    for condition in conditions or []:
        if condition.type in ("Complete", "Failed") and condition.status == "True":
            return condition.type
    return None


def _completion_index(pod: Pod) -> int:
    """The pod's shard (Indexed Jobs); 0 for the single pod of an unsharded run."""
    annotations = (pod.metadata.annotations if pod.metadata else None) or {}
    index = annotations.get(COMPLETION_INDEX_ANNOTATION, "0")
    return int(index) if index.isdigit() else 0


def _leader_first(pods: list[Pod]) -> list[Pod]:
    """Shard 0 first, then the others; newest first within one index."""
    return sorted(pods, key=lambda pod: (_completion_index(pod), -_created_at(pod)))


def _created_at(pod: Pod) -> float:
    created = pod.metadata.creationTimestamp if pod.metadata else None
    return created.timestamp() if created else 0.0
