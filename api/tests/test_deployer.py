"""The managed-target deployer: manifests, ownership, secrets, boots, readiness.

A fake cluster stands in for Kubernetes; the boot-failure lines are the stems
recorded from vLLM v0.34.0's source.
"""

from pathlib import Path
from typing import Any

import pytest
from lightkube import codecs
from pydantic import ValidationError

from servtuner_api.deployer.base import DeployInputError, ObjectRef, OwnershipError
from servtuner_api.deployer.boot import (
    BOOT_RULES,
    classify_boot_failure,
    parse_kv_sizing,
    user_facing,
)
from servtuner_api.deployer.kubernetes import Deployer, LightkubeManagedCluster
from servtuner_api.deployer.manifests import render_manifests, serving_args
from servtuner_api.managed_targets.runs import run_target
from servtuner_api.managed_targets.spec import ManagedTargetSpec
from servtuner_api.resources.targets import TargetCheckView
from servtuner_api.secret_store import FileSecretStore
from tests.support.managed import NAME, NAMESPACE, FakeChecker, FakeCluster, check_view

TARGET_ID = "01JMANAGED0000000000000000"
LABELS = {"app.kubernetes.io/managed-by": "servtuner", "servtuner.dev/managed-target": NAME}
KNOB_ARGS = ["--max-num-seqs=256"]

BASE: dict[str, Any] = {
    "image": "vllm/vllm-openai:v0.34.0",
    "model": "Qwen/Qwen3-32B",
    "gpus": 2,
    "cpu": "16",
    "memory": "128Gi",
    "hf_token": {"name": "hf-token", "key": "token"},
    "base_flags": ["--max-model-len=32768"],
    "model_cache": {"size": "200Gi"},
}
PLAIN = ManagedTargetSpec.model_validate(BASE)
TLS = ManagedTargetSpec.model_validate(
    BASE
    | {"transport": {"enabled": True, "issuer": {"kind": "ClusterIssuer", "name": "servtuner-ca"}}}
)


def deployer(cluster: FakeCluster, tmp_path: Path, view: TargetCheckView | None = None) -> Deployer:
    return Deployer(
        cluster=cluster,
        render_knobs=lambda knobs, server: list(KNOB_ARGS),
        checker=FakeChecker(view or check_view("ok")),
        store=FileSecretStore(tmp_path / "store"),
        prometheus={"url": "http://prometheus.example:9090", "tls": {"enabled": False}},
    )


def by_kind(manifests: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    return {manifest["kind"]: manifest for manifest in manifests}


def container(manifests: list[dict[str, Any]]) -> dict[str, Any]:
    return by_kind(manifests)["Deployment"]["spec"]["template"]["spec"]["containers"][0]


# --- manifests


def test_plain_http_manifests() -> None:
    args = serving_args(NAME, PLAIN, KNOB_ARGS)
    manifests = render_manifests(NAME, NAMESPACE, PLAIN, args)
    assert [m["kind"] for m in manifests] == [
        "PersistentVolumeClaim",
        "Service",
        "PodMonitor",
        "Deployment",
    ]
    assert all(
        m["metadata"]["labels"] == LABELS and m["metadata"]["namespace"] == NAMESPACE
        for m in manifests
    )
    deployment = by_kind(manifests)["Deployment"]["spec"]
    assert deployment["replicas"] == 1
    assert deployment["strategy"] == {"type": "Recreate"}
    vllm = container(manifests)
    assert vllm["args"] == [
        "Qwen/Qwen3-32B",
        "--host=0.0.0.0",
        "--port=8000",
        "--download-dir=/models/hf",
        "--max-model-len=32768",
        "--max-num-seqs=256",
    ]
    assert vllm["resources"]["limits"]["nvidia.com/gpu"] == "2"
    assert vllm["startupProbe"]["httpGet"]["scheme"] == "HTTP"
    assert {
        "name": "HF_TOKEN",
        "valueFrom": {"secretKeyRef": {"name": "hf-token", "key": "token"}},
    } in vllm["env"]
    endpoint = by_kind(manifests)["PodMonitor"]["spec"]["podMetricsEndpoints"][0]
    assert endpoint["scheme"] == "http" and "tlsConfig" not in endpoint
    # The series must land under job="servtuner-targets", where the catalogs look.
    assert by_kind(manifests)["PodMonitor"]["spec"]["jobLabel"] == "servtuner.dev/scrape-job"
    pod_labels = by_kind(manifests)["Deployment"]["spec"]["template"]["metadata"]["labels"]
    assert pod_labels["servtuner.dev/scrape-job"] == "servtuner-targets"
    assert not any("ssl" in arg for arg in vllm["args"])


def test_a_mirror_or_a_seeded_claim_instead_of_hugging_face() -> None:
    """An install with no internet access names where the weights come from."""
    mirror = ManagedTargetSpec.model_validate(
        BASE | {"model_source": {"kind": "mirror", "endpoint": "https://hf.example.internal"}}
    )
    vllm = container(render_manifests(NAME, NAMESPACE, mirror, []))
    assert {"name": "HF_ENDPOINT", "value": "https://hf.example.internal"} in vllm["env"]

    seeded = ManagedTargetSpec.model_validate(
        {k: v for k, v in BASE.items() if k != "model_cache"}
        | {"model_source": {"kind": "pvc", "claim": "qwen3-32b-weights"}}
    )
    manifests = render_manifests(NAME, NAMESPACE, seeded, [], image_pull_secrets=("regcred",))
    assert "PersistentVolumeClaim" not in by_kind(
        manifests
    )  # the operator's claim, never servtuner's
    pod = by_kind(manifests)["Deployment"]["spec"]["template"]["spec"]
    assert {"name": "HF_HUB_OFFLINE", "value": "1"} in pod["containers"][0]["env"]
    assert {
        "name": "model-cache",
        "persistentVolumeClaim": {"claimName": "qwen3-32b-weights"},
    } in pod["volumes"]
    assert pod["imagePullSecrets"] == [{"name": "regcred"}]


def test_an_offline_install_never_lets_flashinfer_download() -> None:
    offline = container(render_manifests(NAME, NAMESPACE, PLAIN, [], offline=True))["env"]
    assert {"name": "FLASHINFER_NO_DOWNLOAD", "value": "1"} in offline
    online = container(render_manifests(NAME, NAMESPACE, PLAIN, []))["env"]
    assert not any(item["name"] == "FLASHINFER_NO_DOWNLOAD" for item in online)
    # A target that sets it itself (or mirrors the kernel repository) keeps its own value.
    mirrored = ManagedTargetSpec.model_validate(BASE | {"env": {"FLASHINFER_NO_DOWNLOAD": ""}})
    env = container(render_manifests(NAME, NAMESPACE, mirrored, [], offline=True))["env"]
    assert [item for item in env if item["name"] == "FLASHINFER_NO_DOWNLOAD"] == [
        {"name": "FLASHINFER_NO_DOWNLOAD", "value": ""}
    ]


def test_weights_streamed_from_an_object_store() -> None:
    """vLLM's Run:ai Model Streamer reads them; no cache volume, no Hugging Face."""
    streamed = ManagedTargetSpec.model_validate(
        {k: v for k, v in BASE.items() if k != "model_cache"}
        | {
            "model_source": {
                "kind": "object_store",
                "uri": "s3://models/qwen3-32b",
                "endpoint": "https://rustfs.example.internal:9000",
                "credentials": {"name": "model-store"},
            }
        }
    )
    args = serving_args(NAME, streamed, KNOB_ARGS)
    assert args[:5] == [
        "s3://models/qwen3-32b",
        "--host=0.0.0.0",
        "--port=8000",
        "--load-format=runai_streamer",
        "--served-model-name=Qwen/Qwen3-32B",
    ]
    manifests = render_manifests(NAME, NAMESPACE, streamed, args)
    assert "PersistentVolumeClaim" not in by_kind(manifests)
    pod = by_kind(manifests)["Deployment"]["spec"]["template"]["spec"]
    env = {item["name"]: item for item in pod["containers"][0]["env"]}
    assert env["AWS_ENDPOINT_URL"]["value"] == "https://rustfs.example.internal:9000"
    assert env["RUNAI_STREAMER_S3_USE_VIRTUAL_ADDRESSING"]["value"] == "0"
    assert env["AWS_ACCESS_KEY_ID"]["valueFrom"]["secretKeyRef"] == {
        "name": "model-store",
        "key": "access_key_id",
    }
    assert env["HF_HUB_OFFLINE"]["value"] == "1"
    assert {"name": "model-cache", "emptyDir": {}} in pod["volumes"]


def test_a_streamed_model_on_its_pods_identity_names_no_keys() -> None:
    ambient = ManagedTargetSpec.model_validate(
        {k: v for k, v in BASE.items() if k != "model_cache"}
        | {"model_source": {"kind": "object_store", "uri": "s3://models/qwen3-32b"}}
    )
    env = container(render_manifests(NAME, NAMESPACE, ambient, []))["env"]
    assert not any(item["name"].startswith("AWS_") for item in env)


@pytest.mark.parametrize(
    ("change", "problem"),
    [
        ({"model_source": {"kind": "pvc", "claim": "weights"}}, "drop model_cache"),
        (
            {"model_source": {"kind": "object_store", "uri": "s3://m/q"}, "model_cache": None},
            "",
        ),
        (
            {
                "model_source": {
                    "kind": "object_store",
                    "uri": "gs://m/q",
                    "credentials": {"name": "k"},
                },
                "model_cache": None,
            },
            "s3:// only",
        ),
        (
            {
                "model_source": {"kind": "object_store", "uri": "s3://m/q"},
                "model_cache": None,
                "base_flags": ["--load-format=safetensors"],
            },
            "sets --load-format itself",
        ),
        ({"model_cache": None}, "model_cache is required"),
        ({"env": {"HF_HUB_OFFLINE": "1"}}, "servtuner sets HF_HUB_OFFLINE"),
    ],
)
def test_one_model_cache_and_servtuners_own_variables(change: dict[str, Any], problem: str) -> None:
    document = {k: v for k, v in (BASE | change).items() if v is not None}
    if not problem:  # the valid case beside them
        ManagedTargetSpec.model_validate(document)
        return
    with pytest.raises(ValueError, match=problem):
        ManagedTargetSpec.model_validate(document)


def test_dev_shm_is_memory_backed_and_sized_when_asked() -> None:
    """Tensor parallel workers (NCCL) and large models exchange data through /dev/shm."""

    def shm_volume(spec: ManagedTargetSpec) -> dict[str, Any]:
        manifests = render_manifests(NAME, NAMESPACE, spec, serving_args(NAME, spec, KNOB_ARGS))
        volumes = by_kind(manifests)["Deployment"]["spec"]["template"]["spec"]["volumes"]
        assert {"name": "shm", "mountPath": "/dev/shm"} in container(manifests)["volumeMounts"]
        return next(volume for volume in volumes if volume["name"] == "shm")

    assert shm_volume(PLAIN) == {"name": "shm", "emptyDir": {"medium": "Memory"}}
    sized = ManagedTargetSpec.model_validate(BASE | {"shm_size": "16Gi"})
    assert shm_volume(sized) == {
        "name": "shm",
        "emptyDir": {"medium": "Memory", "sizeLimit": "16Gi"},
    }


@pytest.mark.parametrize("shm_size", ["128Gi", "200Gi", "137438953472"])
def test_dev_shm_must_leave_memory_for_vllm(shm_size: str) -> None:
    """A memory-backed volume counts against the pod's memory limit (128Gi here)."""
    with pytest.raises(ValidationError, match="counts against memory 128Gi"):
        ManagedTargetSpec.model_validate(BASE | {"shm_size": shm_size})


def test_tls_serves_its_own_certificate_and_asks_no_client_certificate() -> None:
    manifests = render_manifests(NAME, NAMESPACE, TLS, serving_args(NAME, TLS, KNOB_ARGS))
    assert manifests[0]["kind"] == "Certificate"
    certificate = manifests[0]["spec"]
    assert certificate["secretName"] == "qwen-tls"
    assert "qwen.servtuner-tune-qwen.svc" in certificate["dnsNames"]
    assert "client auth" not in certificate["usages"]
    args = container(manifests)["args"]
    assert "--ssl-certfile=/etc/servtuner/tls/tls.crt" in args
    assert not any(arg.startswith(("--ssl-ca-certs", "--ssl-cert-reqs")) for arg in args)
    assert container(manifests)["readinessProbe"]["httpGet"]["scheme"] == "HTTPS"
    tls_config = by_kind(manifests)["PodMonitor"]["spec"]["podMetricsEndpoints"][0]["tlsConfig"]
    assert tls_config == {
        "ca": {"secret": {"name": "qwen-tls", "key": "ca.crt"}},
        "serverName": "qwen.servtuner-tune-qwen.svc",
    }


def test_lightkube_accepts_every_manifest() -> None:
    LightkubeManagedCluster(client=object())  # registers Certificate and PodMonitor
    for manifest in render_manifests(NAME, NAMESPACE, TLS, serving_args(NAME, TLS, KNOB_ARGS)):
        assert codecs.from_dict(manifest).kind == manifest["kind"]


# --- deploy


def test_deploy_applies_every_object_with_the_rendered_args(tmp_path: Path) -> None:
    cluster = FakeCluster()
    args = deployer(cluster, tmp_path).deploy(NAME, NAMESPACE, PLAIN, {"vllm.max_num_seqs": 256})
    assert args[-1] == "--max-num-seqs=256"
    assert [ref.kind for ref in cluster.applied] == [
        "PersistentVolumeClaim",
        "Service",
        "PodMonitor",
        "Deployment",
    ]


def test_deploy_never_touches_an_object_without_servtuners_labels(tmp_path: Path) -> None:
    cluster = FakeCluster()
    foreign = ObjectRef("v1", "Service", NAMESPACE, NAME)
    cluster.objects[foreign] = {"metadata": {"name": NAME, "labels": {"app": "theirs"}}}
    with pytest.raises(OwnershipError, match="without servtuner's labels"):
        deployer(cluster, tmp_path).deploy(NAME, NAMESPACE, PLAIN, {})
    assert cluster.applied == []


def test_deploy_checks_its_secrets_before_applying(tmp_path: Path) -> None:
    cluster = FakeCluster()
    cluster.secrets.clear()
    user_tls = ManagedTargetSpec.model_validate(
        BASE | {"transport": {"enabled": True, "secret": "qwen-cert"}}
    )
    cluster.secrets[(NAMESPACE, "qwen-cert")] = {"tls.crt": "c", "tls.key": "k"}
    with pytest.raises(DeployInputError) as caught:
        deployer(cluster, tmp_path).deploy(NAME, NAMESPACE, user_tls, {})
    assert caught.value.problems == [
        "Secret servtuner-tune-qwen/hf-token has no key token (hf_token)",
        "Secret servtuner-tune-qwen/qwen-cert has no ca.crt (transport.secret)",
    ]
    assert cluster.applied == []


# --- boot failures


@pytest.mark.parametrize(
    ("line", "rule", "kind"),
    [
        (
            "ValueError: To serve at least one request with the model's max seq len (32768), "
            "(8.0 GiB KV cache is needed, which is larger than the available KV cache "
            "memory (2.1 GiB)",
            "kv-too-small-for-max-model-len",
            "infeasible",
        ),
        ("ValueError: No available memory for the cache blocks.", "no-kv-blocks", "infeasible"),
        (
            "torch.OutOfMemoryError: CUDA out of memory. Tried to allocate 2.00 GiB. "
            "GPU 0 has a total capacity of",
            "cuda-oom",
            "infeasible",
        ),
        (
            "ValueError: Total number of attention heads (40) must be divisible by "
            "tensor parallel size (3).",
            "heads-not-divisible-by-tp",
            "infeasible",
        ),
        (
            "ValueError: World size (4) is larger than the number of available GPUs (2) "
            "in this node.",
            "more-gpus-than-visible",
            "infeasible",
        ),
        (
            "vllm serve: error: unrecognized arguments: --max-num-seq=4",
            "unknown-flag",
            "render_bug",
        ),
        (
            "AssertionError: Error in memory profiling. Initial free memory 70.1 GiB",
            "memory-profiling-race",
            "sharing",
        ),
        (
            "RuntimeError: Artifact not found locally: 69e3a1/fmha/fmhaSm100.cubin (looked"
            " at /root/.cache/flashinfer/cubins/69e3a1/fmha/fmhaSm100.cubin)."
            " FLASHINFER_NO_DOWNLOAD is set — refusing to download.",
            "flashinfer-kernel-missing",
            "deploy_defect",
        ),
        (
            "huggingface_hub.errors.LocalEntryNotFoundError: Cannot find the requested files in the"
            " disk cache and outgoing traffic has been disabled. To enable hf.co look-ups and"
            " downloads online, set 'local_files_only' to False.",
            "model-not-in-offline-cache",
            "deploy_defect",
        ),
        (
            "huggingface_hub.errors.LocalEntryNotFoundError: An error happened while trying to"
            " locate the file on the Hub and we cannot find the requested files.",
            "hub-unreachable",
            "deploy_defect",
        ),
    ],
)
def test_classify_boot_failure(line: str, rule: str, kind: str) -> None:
    # The cause sits well before the generic last line.
    log = "\n".join(["loading..."] * 50 + [line] + ["traceback line"] * 30)
    log += "\nRuntimeError: Engine core initialization failed. See root cause above."
    failure = classify_boot_failure(log, 1, "Error")
    assert (failure.rule, failure.kind) == (rule, kind)
    assert line in failure.excerpt


def test_every_rule_has_a_test_line() -> None:
    tested = {
        "kv-too-small-for-max-model-len",
        "no-kv-blocks",
        "cuda-oom",
        "heads-not-divisible-by-tp",
    }
    tested |= {"more-gpus-than-visible", "unknown-flag", "memory-profiling-race"}
    tested |= {"model-not-in-offline-cache", "hub-unreachable", "flashinfer-kernel-missing"}
    untested = {rule.rule for rule in BOOT_RULES} - tested
    assert untested <= {
        "bad-flag-value",
        "not-enough-free-memory",
        "shard-not-divisible",
        "certificate-files",
    }


def test_host_oom_is_a_deploy_defect_and_an_unknown_exit_says_so() -> None:
    assert classify_boot_failure("", 137, "OOMKilled").rule == "host-oom-killed"
    assert classify_boot_failure("Traceback\nboom", 1, "Error").kind == "unknown"
    assert classify_boot_failure("usage: vllm serve", 2, "Error").kind == "render_bug"


# --- poll


def rolled_out(cluster: FakeCluster, args: list[str]) -> None:
    ref = ObjectRef("apps/v1", "Deployment", NAMESPACE, NAME)
    cluster.objects[ref] = {
        "metadata": {"name": NAME, "generation": 2, "labels": LABELS},
        "status": {"observedGeneration": 2, "updatedReplicas": 1, "readyReplicas": 1},
    }
    cluster.pod_list = [pod("qwen-1", {"running": {}}, ready=True, args=args)]


def pod(
    name: str,
    state: dict[str, Any],
    *,
    ready: bool = False,
    args: list[str] | None = None,
    last: dict[str, Any] | None = None,
) -> dict[str, Any]:
    status: dict[str, Any] = {"name": "vllm", "state": state, "ready": ready}
    if last:
        status["lastState"] = last
    return {
        "metadata": {"name": name},
        "spec": {"containers": [{"name": "vllm", "args": args or []}]},
        "status": {"containerStatuses": [status]},
    }


def test_poll_before_deploy_fails(tmp_path: Path) -> None:
    assert (
        deployer(FakeCluster(), tmp_path).poll(TARGET_ID, NAME, NAMESPACE, PLAIN, []).state
        == "failed"
    )


def test_poll_reports_an_infeasible_boot_with_its_evidence(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    cluster.pod_list = [
        pod(
            "qwen-1",
            {"waiting": {"reason": "CrashLoopBackOff"}},
            last={"terminated": {"exitCode": 1, "reason": "Error"}},
        )
    ]
    cluster.logs["qwen-1"] = (
        "x\nValueError: No available memory for the cache blocks.\nRuntimeError: Engine core"
    )
    readiness = deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args)
    assert readiness.state == "infeasible"
    assert readiness.failure is not None and readiness.failure.rule == "no-kv-blocks"


def test_poll_fails_on_a_rendering_bug_and_a_pull_error(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    cluster.pod_list = [pod("qwen-1", {"terminated": {"exitCode": 2, "reason": "Error"}})]
    cluster.logs["qwen-1"] = "usage: vllm serve\nvllm serve: error: unrecognized arguments: --nope"
    assert deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args).failure.kind == "render_bug"  # type: ignore[union-attr]
    cluster.pod_list = [
        pod("qwen-1", {"waiting": {"reason": "ImagePullBackOff", "message": "not found"}})
    ]
    readiness = deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args)
    assert (readiness.state, readiness.reason) == ("failed", "ImagePullBackOff: not found")


def test_a_restarted_instance_that_runs_is_still_pending(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    cluster.pod_list = [pod("qwen-1", {"running": {}}, last={"terminated": {"exitCode": 1}})]
    assert deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args).state == "pending"


def test_ready_needs_the_rendered_args_and_check_target(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    rolled_out(cluster, args[:-1])
    assert (
        "other args than rendered" in deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args).reason
    )
    rolled_out(cluster, args)
    readiness = deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args)
    assert readiness.state == "ready"
    request = deployed.checker.requests[-1]  # type: ignore[attr-defined]
    assert request["target"]["endpoint"] == "http://qwen.servtuner-tune-qwen.svc:8000/v1"
    assert request["prometheus"]["url"] == "http://prometheus.example:9090"


def test_metrics_not_yet_visible_keep_it_pending(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path, check_view("failed", failed="metrics"))
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    rolled_out(cluster, args)
    readiness = deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args)
    assert (readiness.state, readiness.reason) == (
        "pending",
        "check-target not passing yet: metrics",
    )


def test_a_warning_does_not_hold_the_run_back(tmp_path: Path) -> None:
    """On a node without DCGM, check-target warns about GPU metrics forever."""
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path, check_view("warning", warned="gpu_metrics"))
    args = deployed.deploy(NAME, NAMESPACE, PLAIN, {})
    rolled_out(cluster, args)
    readiness = deployed.poll(TARGET_ID, NAME, NAMESPACE, PLAIN, args)
    assert readiness.state == "ready"
    assert readiness.reason.endswith("(warnings: gpu_metrics)")


def test_tls_copies_the_ca_for_bench_before_checking(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    args = deployed.deploy(NAME, NAMESPACE, TLS, {})
    rolled_out(cluster, args)
    assert "has no ca.crt" in deployed.poll(TARGET_ID, NAME, NAMESPACE, TLS, args).reason
    cluster.secrets[(NAMESPACE, "qwen-tls")] = {"tls.crt": "c", "tls.key": "k", "ca.crt": "CA PEM"}
    assert deployed.poll(TARGET_ID, NAME, NAMESPACE, TLS, args).state == "ready"
    assert deployed.store.read("servtuner-managed-qwen-ca") == {"ca.crt": "CA PEM"}  # type: ignore[union-attr]
    assert deployed.checker.requests[-1]["target"]["tls"]["caSecretRef"] == {  # type: ignore[attr-defined]
        "name": "servtuner-managed-qwen-ca"
    }


# --- scale and teardown


def test_scale_to_zero_and_teardown_touch_only_owned_objects(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    deployed.deploy(NAME, NAMESPACE, TLS, {})
    deployed.scale_to_zero(NAME, NAMESPACE)
    assert cluster.scaled == [(NAMESPACE, NAME, 0)]
    deployed.store.put("servtuner-managed-qwen-ca", {"ca.crt": "x"}, TARGET_ID)  # type: ignore[union-attr]
    deployed.teardown(NAME, NAMESPACE, TLS)
    assert [ref.kind for ref in cluster.deleted] == [
        "Deployment",
        "PodMonitor",
        "Service",
        "Certificate",
    ]
    assert deployed.store.read("servtuner-managed-qwen-ca") is None  # type: ignore[union-attr]
    cluster.objects[ObjectRef("apps/v1", "Deployment", NAMESPACE, NAME)] = {
        "metadata": {"labels": {}}
    }
    with pytest.raises(OwnershipError):
        deployed.scale_to_zero(NAME, NAMESPACE)


# --- boot probes


def test_a_boot_probe_reports_kv_capacity_or_why_it_did_not_boot(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    knobs = {"vllm.tensor_parallel_size": 2}
    deployment = {"knobs": knobs, "args": deployed.deploy(NAME, NAMESPACE, PLAIN, knobs)}
    readiness, result = deployed.boot_result(TARGET_ID, NAME, NAMESPACE, PLAIN, deployment, 4096)
    assert (readiness.state, result) == ("pending", None)

    rolled_out(cluster, deployment["args"])
    readiness, result = deployed.boot_result(TARGET_ID, NAME, NAMESPACE, PLAIN, deployment, 4096)
    assert readiness.state == "ready"
    assert result == {"knobs": knobs, "outcome": "feasible", "num_gpu_blocks": 8192, "c_kv": 32.0}

    cluster.logs["qwen-1"] = SIZING_LOG_075
    _, result = deployed.boot_result(TARGET_ID, NAME, NAMESPACE, PLAIN, deployment, 4096)
    assert result is not None and result["kv_sizing"]["bound"] == "memory"

    cluster.pod_list = [
        pod(
            "qwen-2",
            {"waiting": {"reason": "CrashLoopBackOff"}},
            last={"terminated": {"exitCode": 1}},
        )
    ]
    cluster.logs["qwen-2"] = "ValueError: No available memory for the cache blocks."
    readiness, result = deployed.boot_result(TARGET_ID, NAME, NAMESPACE, PLAIN, deployment, 4096)
    assert readiness.state == "infeasible"
    assert result is not None and result["outcome"] == "infeasible"
    assert result["reason"].startswith("no-kv-blocks:")


# --- the community Prometheus chart, env and CPU-only targets


def test_scrape_annotations_replace_the_podmonitor() -> None:
    manifests = render_manifests(
        NAME, NAMESPACE, PLAIN, serving_args(NAME, PLAIN, []), "scrape-annotations"
    )
    assert "PodMonitor" not in [m["kind"] for m in manifests]
    template = by_kind(manifests)["Deployment"]["spec"]["template"]["metadata"]
    assert template["labels"]["servtuner.dev/scrape"] == "true"
    assert template["annotations"] == {
        "servtuner.dev/metrics-port": "8000",
        "servtuner.dev/metrics-scheme": "http",
        "servtuner.dev/metrics-path": "/metrics",
    }
    # The selector still matches: the scrape label is an extra one.
    selector = by_kind(manifests)["Deployment"]["spec"]["selector"]["matchLabels"]
    assert selector.items() <= template["labels"].items()


def test_tls_needs_the_podmonitor(tmp_path: Path) -> None:
    cluster = FakeCluster()
    deployed = deployer(cluster, tmp_path)
    deployed.monitor = "scrape-annotations"
    with pytest.raises(DeployInputError, match="needs the podmonitor monitor"):
        deployed.deploy(NAME, NAMESPACE, TLS, {})
    assert cluster.applied == []


def test_env_and_a_cpu_only_target() -> None:
    spec = ManagedTargetSpec.model_validate(
        BASE | {"gpus": 0, "env": {"VLLM_MOCK_BOOT_SECONDS": "5"}}
    )
    vllm = container(render_manifests(NAME, NAMESPACE, spec, serving_args(NAME, spec, [])))
    assert "nvidia.com/gpu" not in vllm["resources"]["requests"]
    assert "nvidia.com/gpu" not in vllm["resources"]["limits"]
    assert {"name": "VLLM_MOCK_BOOT_SECONDS", "value": "5"} in vllm["env"]


@pytest.mark.parametrize("env", [{"HF_TOKEN": "x"}, {"HF_HOME": "/x"}, {"1BAD": "x"}])
def test_env_refuses_servtuners_variables_and_bad_names(env: dict[str, str]) -> None:
    with pytest.raises(ValueError):
        ManagedTargetSpec.model_validate(BASE | {"env": env})


# vLLM 0.34's sizing lines on the RTX 3070 at gpu_memory_utilization 0.75, as logged,
# with the EngineCore prefix.
SIZING_LOG_075 = (
    "(EngineCore pid=33448) INFO 11-23 21:24:41 [gpu_worker.py:1175] Free memory on device"
    " (6.87/8.0 GiB) on startup. Desired GPU memory utilization is (0.75, 6.0 GiB).\n"
    "(EngineCore pid=33448) INFO 11-23 21:24:42 [extensible_kv_cache.py:693] Memory after"
    " warmup: 5.25 GiB free, 0.05 GiB committed to the KV cache, 0.37 GiB kept for transient"
    " activations (0.3 GiB allocated at peak, 0.37 GiB of torch reservation growth); 5312 KV"
    " cache blocks (3.89 GiB) fit within the requested 6.0 GiB. Torch may reserve up to 2.14"
    " GiB.\n"
)


def test_kv_sizing_names_the_limit_that_sized_the_cache() -> None:
    assert parse_kv_sizing(SIZING_LOG_075) == {
        "bound": "memory",  # 6.0 - 1.57 = 4.43 GiB allowed, 5.30 - 1 = 4.30 GiB free
        "kv_cache_gib": 3.89,
        "requested_gib": 6.0,
        "memory_limit_gib": 4.3,
        "utilization_limit_gib": 4.43,
    }
    # The same boot at 0.6: 4.8 - 1.57 = 3.23 GiB allowed is below the free 4.30 GiB.
    at_06 = SIZING_LOG_075.replace("(3.89 GiB)", "(2.81 GiB)").replace(
        "requested 6.0 GiB", "requested 4.8 GiB"
    )
    sizing = parse_kv_sizing(at_06)
    assert sizing is not None and (sizing["bound"], sizing["utilization_limit_gib"]) == (
        "utilization",
        3.23,
    )
    assert parse_kv_sizing("INFO GPU KV cache size: 339,968 tokens") is None  # older vLLM


def test_a_reason_leaves_out_developer_links() -> None:
    line = (
        "pydantic_core._pydantic_core.ValidationError: 1 validation error for ParallelConfig "
        "Value error, World size (2) is larger than the number of available GPUs (1) in this "
        "node. [type=value_error, input_value=ArgsKwargs(()), input_type=ArgsKwargs] "
        "For further information visit https://errors.pydantic.dev/2.14/v/value_error"
    )
    assert user_facing(line).endswith("input_type=ArgsKwargs]")
    assert user_facing("see (https://docs.vllm.ai/en/latest/x.html) for more") == "see for more"
    assert user_facing("no links here") == "no links here"


SGLANG: dict[str, Any] = BASE | {
    "server": "sglang",
    "image": "lmsysorg/sglang:v0.5.23",
    "base_flags": ["--context-length=32768"],
}


def test_a_stored_target_without_a_server_is_vllm() -> None:
    assert PLAIN.server == "vllm"
    assert container(render_manifests(NAME, NAMESPACE, PLAIN, []))["command"] == ["vllm", "serve"]


def test_sglang_manifests_run_its_server_with_metrics_on() -> None:
    spec = ManagedTargetSpec.model_validate(SGLANG | {"served_model_name": "qwen"})
    args = serving_args(NAME, spec, ["--mem-fraction-static=0.8"])
    assert args == [
        "--model-path=Qwen/Qwen3-32B",
        "--host=0.0.0.0",
        "--port=8000",
        "--download-dir=/models/hf",
        "--enable-metrics",
        "--served-model-name=qwen",
        "--context-length=32768",
        "--mem-fraction-static=0.8",
    ]
    serving = container(render_manifests(NAME, NAMESPACE, spec, args))
    assert serving["command"] == ["python3", "-m", "sglang.launch_server"]
    assert serving["args"] == args
    # /health answers 503 while SGLang starts, so the probes work as they do for vLLM.
    assert serving["startupProbe"]["httpGet"]["path"] == "/health"


def test_sglang_tls_is_a_reloaded_server_certificate() -> None:
    spec = ManagedTargetSpec.model_validate(
        SGLANG
        | {
            "transport": {
                "enabled": True,
                "issuer": {"kind": "ClusterIssuer", "name": "servtuner-ca"},
            }
        }
    )
    args = serving_args(NAME, spec, [])
    assert "--ssl-certfile=/etc/servtuner/tls/tls.crt" in args
    assert "--enable-ssl-refresh" in args
    assert not any(flag.startswith("--ssl-ca-certs") for flag in args)


@pytest.mark.parametrize(
    ("change", "problem"),
    [
        ({"base_flags": ["--port=9000"]}, "servtuner sets --port itself"),
        ({"base_flags": ["--model-path=x"]}, "servtuner sets --model-path itself"),
        ({"base_flags": ["--enable-metrics"]}, "servtuner sets --enable-metrics itself"),
        (
            {
                "model_cache": None,
                "model_source": {"kind": "object_store", "uri": "s3://weights/qwen"},
            },
            "cannot stream from an object store",
        ),
    ],
)
def test_sglang_refuses_owned_flags_and_streaming(change: dict[str, Any], problem: str) -> None:
    with pytest.raises(ValidationError, match=problem):
        ManagedTargetSpec.model_validate(SGLANG | change)


def test_a_managed_sglangs_runs_name_an_sglang_target() -> None:
    target = run_target(NAME, NAMESPACE, ManagedTargetSpec.model_validate(SGLANG))
    assert target["type"] == "sglang"
    assert run_target(NAME, NAMESPACE, PLAIN)["type"] == "vllm"


@pytest.mark.parametrize(
    ("line", "rule", "kind"),
    [
        (
            "RuntimeError: Not enough memory. Please try to increase --mem-fraction-static.",
            "kv-pool-empty",
            "infeasible",
        ),
        (
            "Possible solutions:\n"
            "1. set --mem-fraction-static to a smaller value (e.g., 0.8 or 0.7)",
            "cuda-graph-oom",
            "infeasible",
        ),
        (
            "torch.OutOfMemoryError: CUDA out of memory. Tried to allocate 2.00 GiB",
            "cuda-oom",
            "infeasible",
        ),
        (
            "ValueError: User-specified context_length (65536) is greater than the derived"
            " context_length (32768).",
            "context-length-too-long",
            "infeasible",
        ),
        (
            "Rank 0 scheduler died during initialization (exit code: -9). If exit code is -9"
            " (SIGKILL), a common cause is the OS OOM killer.",
            "scheduler-oom-killed",
            "deploy_defect",
        ),
        (
            "launch_server.py: error: unrecognized arguments: --max-num-seqs=8",
            "unknown-flag",
            "render_bug",
        ),
    ],
)
def test_classify_sglang_boot_failure(line: str, rule: str, kind: str) -> None:
    log = "\n".join(["loading..."] * 50 + [line] + ["traceback line"] * 30)
    log += "\nInitialization failed. Please see the error messages above."
    failure = classify_boot_failure(log, 1, "Error", "sglang")
    assert (failure.rule, failure.kind) == (rule, kind)


def test_an_unknown_sglang_failure_names_sglang() -> None:
    failure = classify_boot_failure("something odd\n", 1, "Error", "sglang")
    assert failure.rule == "unrecognized" and "SGLang exited" in failure.message
    vllm_rule = "No available memory for the cache blocks"
    assert classify_boot_failure(vllm_rule, 1, "Error", "sglang").rule == "unrecognized"
