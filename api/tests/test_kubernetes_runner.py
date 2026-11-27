"""KubernetesJobRunner against a stand-in cluster: the Job it builds, and how it reads the outcome.

The real API server is exercised on kind (e2e/cluster.py --runner kubernetes).
"""

import dataclasses
import json
from collections.abc import Iterator
from dataclasses import replace
from datetime import UTC, datetime
from pathlib import PurePosixPath

import pytest
from lightkube.models.batch_v1 import JobCondition, JobStatus
from lightkube.models.core_v1 import (
    ContainerState,
    ContainerStateTerminated,
    ContainerStateWaiting,
    ContainerStatus,
    PodCondition,
    PodStatus,
)
from lightkube.models.meta_v1 import ObjectMeta, OwnerReference
from lightkube.resources.batch_v1 import Job
from lightkube.resources.core_v1 import ConfigMap, Pod, Secret

from servtuner_api.kubernetes_runner.config import KubernetesRunnerConfig, job_name, workloads_in
from servtuner_api.kubernetes_runner.jobs import JobBuilder
from servtuner_api.kubernetes_runner.runner import KubernetesJobRunner, LaunchError
from servtuner_api.runner import LaunchRequest, RunHandle, RunnerState
from servtuner_api.secret_store import secret_names_in
from servtuner_api.settings.models import (
    AntiAffinity,
    ChecksumMode,
    CredentialsSource,
    HopTls,
    KubernetesRunnerSettings,
    S3Settings,
)

RUN_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
NAME = "bench-01arz3ndektsv4rrffq69g5fav"
SPEC = {
    "name": "t",
    "target": {
        "type": "bifrost",
        "endpoint": "https://bifrost.example.invalid/v1",
        "workload": {"namespace": "llm", "selector": {"app": "bifrost"}},
        "upstreams": [
            {
                "name": "vllm",
                "endpoint": "http://vllm.example.invalid/v1",
                "workload": {"namespace": "gpu", "selector": {"app": "vllm"}},
                "tls": {"enabled": False},
            }
        ],
        "tls": {"enabled": True, "clientCert": {"secretRef": {"name": "bifrost-client"}}},
        "admin": {"secretRef": {"name": "bifrost-admin", "tokenKey": "token"}},
    },
}
CLUSTER = KubernetesRunnerSettings(
    namespace="servtuner-runs",
    bench_image="registry.example.invalid/servtuner-bench:1.0.0",
    service_account="servtuner-bench",
    bench_cpu="2",
    bench_memory="2Gi",
    target_anti_affinity=AntiAffinity.REQUIRED,
    job_ttl_seconds=3600,
    start_timeout_seconds=300,
    image_pull_secrets=("pull",),
    data_claim="servtuner-data",
    data_fs_group=10001,
    node_pinned=False,
    api_component_label=("app.kubernetes.io/component", "api"),
    api_namespace="servtuner",
    bench_tls_secret="servtuner-bench-tls",
    ca_secret="servtuner-api-tls",
)


PLAIN = HopTls(enabled=False, client_cert=False)
TLS_ONLY = HopTls(enabled=True, client_cert=False)
MUTUAL = HopTls(enabled=True, client_cert=True)


def config(
    hop: HopTls = MUTUAL, cluster: KubernetesRunnerSettings = CLUSTER
) -> KubernetesRunnerConfig:
    return KubernetesRunnerConfig(
        cluster=cluster,
        data_dir=PurePosixPath("/var/lib/servtuner"),
        api_url="https://servtuner-api.servtuner.svc:8080",
        api_hop=hop,
    )


class FakeCluster:
    """Records what the runner creates and plays back Job and pod states."""

    def __init__(self, secrets: set[str] | None = None) -> None:
        self.secrets = secrets if secrets is not None else set(secret_names_in(SPEC))
        self.secrets |= {"servtuner-bench-tls", "servtuner-api-tls"}
        self.jobs: dict[str, Job] = {}
        self.config_maps: list[ConfigMap] = []
        self.created_secrets: list[Secret] = []
        self.pods: list[Pod] = []
        self.deleted: list[str] = []
        self.fail_config_map = False

    def secret_exists(self, name: str) -> bool:
        return name in self.secrets

    def create_job(self, job: Job) -> OwnerReference:
        assert job.metadata is not None and job.metadata.name is not None
        self.jobs[job.metadata.name] = job
        return OwnerReference(apiVersion="batch/v1", kind="Job", name=job.metadata.name, uid="u-1")

    def create_config_map(self, config_map: ConfigMap) -> None:
        if self.fail_config_map:
            raise RuntimeError("API server said no")
        self.config_maps.append(config_map)

    def create_secret(self, secret: Secret) -> None:
        self.created_secrets.append(secret)

    def get_job(self, name: str) -> Job | None:
        return self.jobs.get(name)

    def delete_job(self, name: str) -> None:
        self.deleted.append(name)
        self.jobs.pop(name, None)

    def list_pods(self, run_id: str) -> list[Pod]:
        return self.pods

    def pod_log(self, pod_name: str) -> Iterator[str]:
        yield from (f"{pod_name} line 1\n", "line 2\n")

    def finish(self, condition: str) -> None:
        self.jobs[NAME].status = JobStatus(conditions=[JobCondition(type=condition, status="True")])


class Clock:
    def __init__(self) -> None:
        self.now = 1000.0

    def __call__(self) -> float:
        return self.now


def launch(
    cluster: FakeCluster,
    runner_config: KubernetesRunnerConfig | None = None,
    clock: Clock | None = None,
) -> tuple[KubernetesJobRunner, RunHandle]:
    runner = KubernetesJobRunner(runner_config or config(), cluster, clock or Clock())
    handle = runner.launch(LaunchRequest(RUN_ID, json.dumps(SPEC).encode(), "secret-token"))
    return runner, handle


def pod(
    *,
    phase: str = "Running",
    terminated: ContainerStateTerminated | None = None,
    waiting: ContainerStateWaiting | None = None,
    conditions: list[PodCondition] | None = None,
) -> Pod:
    state = ContainerState(terminated=terminated, waiting=waiting)
    return Pod(
        apiVersion="v1",
        kind="Pod",
        metadata=ObjectMeta(
            name=f"{NAME}-abcde", creationTimestamp=datetime(2026, 10, 30, tzinfo=UTC)
        ),
        status=PodStatus(
            phase=phase,
            conditions=conditions,
            containerStatuses=[
                ContainerStatus(
                    name="bench", image="i", imageID="", ready=False, restartCount=0, state=state
                )
            ],
        ),
    )


def test_names_and_spec_walkers() -> None:
    assert job_name(RUN_ID) == NAME
    assert secret_names_in(SPEC) == ["bifrost-admin", "bifrost-client"]
    assert [(w.namespace, w.selector) for w in workloads_in(SPEC)] == [
        ("llm", {"app": "bifrost"}),
        ("gpu", {"app": "vllm"}),
    ]
    assert workloads_in({"target": {"type": "vllm"}}) == []


@pytest.mark.parametrize("hop", [PLAIN, TLS_ONLY, MUTUAL])
def test_command_follows_the_bench_contract(hop: HopTls) -> None:
    args = JobBuilder(config(hop)).command(RUN_ID)
    expected = [
        "run", "--spec", "/etc/servtuner/spec/spec.json",
        "--out", f"/var/lib/servtuner/runs/{RUN_ID}", "--run-id", RUN_ID,
        "--api-url", "https://servtuner-api.servtuner.svc:8080",
        "--run-token-file", "/etc/servtuner/run-token/token",
        "--api-tls", "true" if hop.enabled else "false",
    ]  # fmt: skip
    if hop.enabled:
        expected += ["--tls-ca-file", "/etc/servtuner/tls/ca/ca.crt"]
    if hop.client_cert:
        expected += [
            "--tls-cert-file", "/etc/servtuner/tls/bench/tls.crt",
            "--tls-key-file", "/etc/servtuner/tls/bench/tls.key",
        ]  # fmt: skip
    assert args == expected


def test_an_offline_install_runs_bench_offline() -> None:
    assert "--offline" not in JobBuilder(config()).command(RUN_ID)
    offline = dataclasses.replace(config(), offline=True)
    assert "--offline" in JobBuilder(offline).command(RUN_ID)


def test_job_is_guaranteed_never_retried_and_cleaned_up() -> None:
    job = JobBuilder(config()).job(RUN_ID, SPEC)
    assert job.spec is not None and job.spec.template.spec is not None
    assert (job.spec.backoffLimit, job.spec.ttlSecondsAfterFinished) == (0, 3600)
    pod_spec = job.spec.template.spec
    assert pod_spec.restartPolicy == "Never"
    assert pod_spec.serviceAccountName == "servtuner-bench"
    [container] = pod_spec.containers
    assert container.resources is not None
    assert (
        container.resources.requests == container.resources.limits == {"cpu": "2", "memory": "2Gi"}
    )
    assert (
        container.securityContext is not None and container.securityContext.readOnlyRootFilesystem
    )
    env = {variable.name for variable in container.env or []}
    assert {"POD_NAME", "POD_NAMESPACE", "NODE_NAME", "SERVTUNER_DATA_DIR"} <= env


def test_every_secret_the_spec_names_is_mounted_where_bench_reads_it() -> None:
    job = JobBuilder(config()).job(RUN_ID, SPEC)
    assert job.spec is not None and job.spec.template.spec is not None
    [container] = job.spec.template.spec.containers
    mounts = {mount.mountPath: mount.name for mount in container.volumeMounts or []}
    volumes = {volume.name: volume for volume in job.spec.template.spec.volumes or []}
    for name in ("bifrost-admin", "bifrost-client"):
        volume = volumes[mounts[f"/etc/servtuner/secrets/{name}"]]
        assert volume.secret is not None and volume.secret.secretName == name
        assert volume.secret.defaultMode == 0o440
    assert volumes[mounts["/var/lib/servtuner"]].persistentVolumeClaim is not None
    assert volumes[mounts["/etc/servtuner/spec"]].configMap is not None
    token = volumes[mounts["/etc/servtuner/run-token"]].secret
    assert token is not None and token.secretName == NAME


def test_no_tls_mounts_no_certificates() -> None:
    builder = JobBuilder(config(PLAIN, replace(CLUSTER, bench_tls_secret=None, ca_secret=None)))
    job = builder.job(RUN_ID, SPEC)
    assert job.spec is not None and job.spec.template.spec is not None
    paths = {mount.mountPath for mount in job.spec.template.spec.containers[0].volumeMounts or []}
    assert not {"/etc/servtuner/tls/bench", "/etc/servtuner/tls/ca"} & paths


@pytest.mark.parametrize("anti_affinity", list(AntiAffinity))
def test_bench_stays_off_every_target_node(anti_affinity: AntiAffinity) -> None:
    job = JobBuilder(config(cluster=replace(CLUSTER, target_anti_affinity=anti_affinity))).job(
        RUN_ID, SPEC
    )
    assert job.spec is not None and job.spec.template.spec is not None
    affinity = job.spec.template.spec.affinity
    assert affinity is not None and affinity.podAntiAffinity is not None
    assert affinity.podAffinity is None  # the volume is not node-pinned
    if anti_affinity is AntiAffinity.REQUIRED:
        terms = affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution or []
    else:
        weighted = affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution or []
        terms = [term.podAffinityTerm for term in weighted]
    assert [
        (term.namespaces, term.labelSelector.matchLabels if term.labelSelector else None)
        for term in terms
    ] == [
        (["llm"], {"app": "bifrost"}),
        (["gpu"], {"app": "vllm"}),
    ]
    assert {term.topologyKey for term in terms} == {"kubernetes.io/hostname"}


def test_a_read_write_once_volume_pins_bench_to_the_api_node() -> None:
    job = JobBuilder(config(cluster=replace(CLUSTER, node_pinned=True))).job(RUN_ID, {"target": {}})
    assert job.spec is not None and job.spec.template.spec is not None
    affinity = job.spec.template.spec.affinity
    assert (
        affinity is not None
        and affinity.podAffinity is not None
        and affinity.podAntiAffinity is None
    )
    [term] = affinity.podAffinity.requiredDuringSchedulingIgnoredDuringExecution or []
    assert term.namespaces == ["servtuner"]
    assert term.labelSelector is not None
    assert term.labelSelector.matchLabels == {"app.kubernetes.io/component": "api"}


def test_launch_creates_the_job_then_its_owned_spec_and_token() -> None:
    cluster = FakeCluster()
    _, handle = launch(cluster)
    assert handle.reference == f"servtuner-runs/{NAME}"
    [config_map], [secret] = cluster.config_maps, cluster.created_secrets
    assert config_map.data == {"spec.json": json.dumps(SPEC)}
    assert secret.stringData == {"token": "secret-token"}
    for created in (config_map, secret):
        assert created.metadata is not None and created.metadata.ownerReferences is not None
        assert [owner.uid for owner in created.metadata.ownerReferences] == ["u-1"]


def test_missing_secrets_fail_the_launch_before_anything_is_created() -> None:
    cluster = FakeCluster(secrets={"bifrost-client"})
    with pytest.raises(LaunchError, match="bifrost-admin"):
        launch(cluster)
    assert not cluster.jobs and not cluster.config_maps


def test_a_failed_launch_removes_the_job() -> None:
    cluster = FakeCluster()
    cluster.fail_config_map = True
    with pytest.raises(RuntimeError):
        launch(cluster)
    assert cluster.deleted == [NAME] and not cluster.jobs


def test_job_conditions_decide_the_final_state() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    assert runner.status(handle).state is RunnerState.RUNNING
    cluster.pods = [pod(terminated=ContainerStateTerminated(exitCode=0, reason="Completed"))]
    cluster.finish("Complete")
    status = runner.status(handle)
    assert (status.state, status.exit_code) == (RunnerState.SUCCEEDED, 0)


def test_an_oom_killed_bench_fails_the_run_with_the_reason() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    cluster.pods = [
        pod(phase="Failed", terminated=ContainerStateTerminated(exitCode=137, reason="OOMKilled"))
    ]
    cluster.finish("Failed")
    status = runner.status(handle)
    assert (status.state, status.exit_code) == (RunnerState.FAILED, 137)
    assert status.message == "OOMKilled (exit code 137)"


def test_an_unschedulable_pod_fails_the_run_after_the_start_timeout() -> None:
    cluster, clock = FakeCluster(), Clock()
    runner, handle = launch(cluster, clock=clock)
    cluster.pods = [
        pod(
            phase="Pending",
            conditions=[
                PodCondition(
                    type="PodScheduled",
                    status="False",
                    reason="Unschedulable",
                    message="0/1 nodes are available: didn't match pod anti-affinity rules.",
                )
            ],
        )
    ]
    clock.now += 299
    assert runner.status(handle).state is RunnerState.RUNNING
    clock.now += 2
    status = runner.status(handle)
    assert status.state is RunnerState.FAILED
    assert status.message is not None and "anti-affinity" in status.message
    assert cluster.deleted == [NAME]


def test_an_image_pull_failure_fails_the_run_after_the_start_timeout() -> None:
    cluster, clock = FakeCluster(), Clock()
    runner, handle = launch(cluster, clock=clock)
    cluster.pods = [
        pod(
            phase="Pending",
            waiting=ContainerStateWaiting(reason="ImagePullBackOff", message="nope"),
        )
    ]
    clock.now += 301
    status = runner.status(handle)
    assert status.state is RunnerState.FAILED
    assert status.message == "the bench pod did not start: ImagePullBackOff: nope"


def test_a_slow_but_healthy_pod_is_never_timed_out() -> None:
    cluster, clock = FakeCluster(), Clock()
    runner, handle = launch(cluster, clock=clock)
    cluster.pods = [pod(phase="Running")]
    clock.now += 10_000
    assert runner.status(handle).state is RunnerState.RUNNING


def test_cancel_deletes_the_job_and_reports_cancelled() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    runner.cancel(handle)
    assert cluster.deleted == [NAME]
    assert runner.status(handle).state is RunnerState.CANCELLED


def test_a_vanished_job_is_failed() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    cluster.jobs.clear()
    status = runner.status(handle)
    assert status.state is RunnerState.FAILED and status.message == "the bench Job no longer exists"


def test_logs_come_from_the_bench_pod() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    assert list(runner.logs(handle)) == []
    cluster.pods = [pod()]
    assert list(runner.logs(handle)) == [f"{NAME}-abcde line 1", "line 2"]


def test_an_s3_store_reaches_the_job_by_reference() -> None:
    s3 = S3Settings(
        endpoint="https://rustfs.example.invalid",
        region="us-east-1",
        bucket="servtuner-runs",
        prefix="runs/",
        force_path_style=True,
        checksum_mode=ChecksumMode.WHEN_REQUIRED,
        credentials_source=CredentialsSource.SECRET,
        access_key_id="never-in-the-job",
        secret_access_key="never-in-the-job",
    )
    cluster = replace(
        CLUSTER, data_claim=None, s3_credentials=("servtuner-s3", "access-key", "secret-key")
    )
    runner_config = replace(config(cluster=cluster), s3=s3, object_store_hop=MUTUAL)
    builder = JobBuilder(runner_config)
    args = builder.command(RUN_ID)
    position = args.index("--out")
    assert args[position : position + 4] == [
        "--out",
        "s3://servtuner-runs/runs/",
        "--object-store-tls",
        "true",
    ]
    assert "--object-store-ca-file" in args and RUN_ID not in args[position + 1]
    job = builder.job(RUN_ID, SPEC)
    assert job.spec is not None and job.spec.template.spec is not None
    [container] = job.spec.template.spec.containers
    env = {variable.name: variable for variable in container.env or []}
    assert env["SERVTUNER_S3_REGION"].value == "us-east-1"
    assert env["SERVTUNER_S3_ENDPOINT"].value == "https://rustfs.example.invalid"
    assert env["SERVTUNER_S3_PART_SIZE_MIB"].value == "16"
    key = env["AWS_SECRET_ACCESS_KEY"].valueFrom
    assert key is not None and key.secretKeyRef is not None
    assert (key.secretKeyRef.name, key.secretKeyRef.key) == ("servtuner-s3", "secret-key")
    assert "never-in-the-job" not in json.dumps(job.to_dict())
    paths = {mount.mountPath for mount in container.volumeMounts or []}
    assert "/var/lib/servtuner" not in paths  # no data volume with s3


SHARDED_SPEC = {**SPEC, "engine": {"type": "loadgen", "shards": 3}}


def shard_pod(index: int, exit_code: int, reason: str = "Completed") -> Pod:
    one = pod(
        phase="Failed", terminated=ContainerStateTerminated(exitCode=exit_code, reason=reason)
    )
    assert one.metadata is not None
    one.metadata.name = f"{NAME}-{index}-abcde"
    one.metadata.annotations = {"batch.kubernetes.io/job-completion-index": str(index)}
    return one


def test_a_sharded_run_is_an_indexed_job_with_one_pod_per_shard() -> None:
    job = JobBuilder(config()).job(RUN_ID, SHARDED_SPEC)
    assert job.spec is not None and job.spec.template.spec is not None
    assert (job.spec.completionMode, job.spec.completions, job.spec.parallelism) == (
        "Indexed",
        3,
        3,
    )
    assert job.spec.backoffLimit == 0
    [container] = job.spec.template.spec.containers
    assert container.args is not None
    flag = container.args.index("--shard-index")
    assert container.args[flag + 1] == "$(JOB_COMPLETION_INDEX)"
    [index_env] = [
        variable for variable in container.env or [] if variable.name == "JOB_COMPLETION_INDEX"
    ]
    assert index_env.valueFrom is not None and index_env.valueFrom.fieldRef is not None
    assert "job-completion-index" in index_env.valueFrom.fieldRef.fieldPath
    affinity = job.spec.template.spec.affinity
    assert affinity is not None and affinity.podAntiAffinity is not None
    spread = affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution or []
    assert any(
        term.podAffinityTerm.labelSelector is not None
        and (term.podAffinityTerm.labelSelector.matchLabels or {}).get("servtuner.dev/run-id")
        == RUN_ID.lower()
        for term in spread
    ), "shards prefer different nodes"


def test_an_unsharded_job_is_unchanged() -> None:
    job = JobBuilder(config()).job(RUN_ID, SPEC)
    assert job.spec is not None and job.spec.completionMode is None
    [container] = job.spec.template.spec.containers  # type: ignore[union-attr]
    assert "--shard-index" not in (container.args or [])


def test_the_leaders_exit_code_and_log_speak_for_a_sharded_run() -> None:
    cluster = FakeCluster()
    runner, handle = launch(cluster)
    cluster.pods = [shard_pod(2, 0), shard_pod(0, 4, "Error"), shard_pod(1, 0)]
    cluster.finish("Failed")
    status = runner.status(handle)
    assert (status.state, status.exit_code) == (RunnerState.FAILED, 4)
    assert next(iter(runner.logs(handle))).startswith(f"{NAME}-0-")
