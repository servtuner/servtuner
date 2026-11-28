"""Managed targets: vLLM templates servtuner deploys itself.

Storage and checks only: the deployer creates the Kubernetes objects.
"""

import copy
from collections.abc import Iterator, Mapping
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.deployer.base import DeployInputError, ObjectRef
from servtuner_api.deployer.kubernetes import Deployer
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.secret_store import FileSecretStore
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.managed import NAME, NAMESPACE, FakeChecker, FakeCluster, check_view

TUNING_NAMESPACES = ("servtuner-tune-qwen", "servtuner-tune-llama")

QWEN: dict[str, Any] = {
    "name": "qwen",
    "description": "Qwen3-32B on the H200 node",
    "namespace": "servtuner-tune-qwen",
    "spec": {
        "image": "vllm/vllm-openai:v0.34.0",
        "model": "Qwen/Qwen3-32B",
        "gpus": 2,
        "cpu": "16",
        "memory": "128Gi",
        "node_selector": {"nvidia.com/gpu.product": "NVIDIA-H200"},
        "tolerations": [{"key": "nvidia.com/gpu", "operator": "Exists", "effect": "NoSchedule"}],
        "hf_token": {"name": "hf-token", "key": "token"},
        "base_flags": ["--max-model-len=32768"],
        "model_cache": {"size": "200Gi"},
    },
}


@pytest.fixture
def client(engine: Engine, data_dir: Path) -> Iterator[TestClient]:
    def service_with_namespaces():  # type: ignore[no-untyped-def]
        service = build_service(engine, FakeRunner(), data_dir)
        service.managed_targets = ManagedTargetService(
            service._repository,  # noqa: SLF001 - the test assembles the service by hand
            TUNING_NAMESPACES,
        )
        return service

    with TestClient(create_app(service_with_namespaces)) as test_client:
        yield test_client


def with_spec(**changes: Any) -> dict[str, Any]:
    body = copy.deepcopy(QWEN)
    body["spec"].update(changes)
    return body


def test_create_defaults_to_plain_http_and_derives_the_run_target(client: TestClient) -> None:
    created = client.post("/managed-targets", json=QWEN)
    assert created.status_code == 201, created.text
    target = created.json()
    assert target["spec"]["transport"] == {"enabled": False}
    assert target["run_target"] == {
        "type": "vllm",
        "endpoint": "http://qwen.servtuner-tune-qwen.svc:8000/v1",
        "model": "Qwen/Qwen3-32B",
        "workload": {
            "namespace": "servtuner-tune-qwen",
            "selector": {"servtuner.dev/managed-target": "qwen"},
        },
        "tls": {"enabled": False},
        "upstreams": [],
    }
    assert target["run_labels"] == {"servtuner.managed-target": target["id"].lower()}
    assert client.get(f"/managed-targets/{target['id']}").json() == target
    assert [listed["name"] for listed in client.get("/managed-targets").json()] == ["qwen"]


def test_tls_uses_the_targets_own_certificate_and_a_ca_copy_for_bench(client: TestClient) -> None:
    body = with_spec(
        transport={"enabled": True, "issuer": {"kind": "ClusterIssuer", "name": "servtuner-ca"}},
        served_model_name="qwen",
    )
    target = client.post("/managed-targets", json=body).json()
    assert target["run_target"]["endpoint"] == "https://qwen.servtuner-tune-qwen.svc:8000/v1"
    assert target["run_target"]["model"] == "qwen"
    assert target["run_target"]["tls"] == {
        "enabled": True,
        "caSecretRef": {"name": "servtuner-managed-qwen-ca"},
    }
    # Client certificates are not offered yet: the policy is none, in the spec's own shape.
    assert target["spec"]["transport"]["client_auth"] == "none"


@pytest.mark.parametrize(
    ("change", "reason"),
    [
        ({"namespace": "default"}, "not a tuning namespace"),
        ({"spec": {**QWEN["spec"], "image": "vllm/vllm-openai"}}, "pin the image"),
        ({"spec": {**QWEN["spec"], "image": "vllm/vllm-openai:latest"}}, "latest tag moves"),
        ({"spec": {**QWEN["spec"], "base_flags": ["--port", "9000"]}}, "servtuner sets --port"),
        ({"spec": {**QWEN["spec"], "base_flags": ["--ssl-certfile=/x"]}}, "--ssl-certfile"),
        ({"spec": {**QWEN["spec"], "gpus": 9}}, "gpus"),
        ({"spec": {**QWEN["spec"], "memory": "lots"}}, "memory"),
        ({"spec": {**QWEN["spec"], "transport": {"mode": "mtls"}}}, "transport"),
        ({"spec": {**QWEN["spec"], "transport": {"enabled": True}}}, "exactly one certificate"),
        (
            {
                "spec": {
                    **QWEN["spec"],
                    "transport": {"enabled": True, "client_auth": "required", "secret": "c"},
                }
            },
            "client_auth",
        ),
        ({"spec": {**QWEN["spec"], "transport": {"enabled": False, "secret": "c"}}}, "transport"),
        ({"spec": {**QWEN["spec"], "hf_token": {"name": "hf", "key": "t", "value": "x"}}}, "value"),
    ],
)
def test_rejects_what_cannot_be_deployed_safely(
    client: TestClient, change: dict[str, Any], reason: str
) -> None:
    response = client.post("/managed-targets", json={**QWEN, **change})
    assert response.status_code == 422
    assert reason in response.text


def test_one_target_per_name_and_per_namespace(client: TestClient) -> None:
    assert client.post("/managed-targets", json=QWEN).status_code == 201
    same_namespace = {**QWEN, "name": "qwen-2"}
    assert client.post("/managed-targets", json=same_namespace).status_code == 409
    same_name = {**QWEN, "namespace": "servtuner-tune-llama"}
    assert client.post("/managed-targets", json=same_name).status_code == 409


def test_update_and_delete(client: TestClient) -> None:
    target = client.post("/managed-targets", json=QWEN).json()
    moved = client.put(
        f"/managed-targets/{target['id']}",
        json={**QWEN, "namespace": "servtuner-tune-llama", "spec": {**QWEN["spec"], "gpus": 1}},
    )
    assert moved.status_code == 200, moved.text
    assert moved.json()["spec"]["gpus"] == 1
    assert moved.json()["run_target"]["workload"]["namespace"] == "servtuner-tune-llama"
    assert client.delete(f"/managed-targets/{target['id']}").status_code == 204
    assert client.get(f"/managed-targets/{target['id']}").status_code == 404


def test_a_kubernetes_target_keeps_no_credentials_here(client: TestClient) -> None:
    """Its Hugging Face token is a Secret in its namespace, named in the spec (hf_token)."""
    target = client.post("/managed-targets", json=QWEN).json()
    assert target["credentials"] == {}
    response = client.put(
        f"/managed-targets/{target['id']}/credentials/api_key", json={"value": "x"}
    )
    assert response.status_code == 422 and "only a host target" in response.text


def test_no_tuning_namespace_means_nothing_can_be_stored(engine: Engine, data_dir: Path) -> None:
    with TestClient(create_app(lambda: build_service(engine, FakeRunner(), data_dir))) as plain:
        response = plain.post("/managed-targets", json=QWEN)
    assert response.status_code == 422
    assert "granted: none" in response.text


# --- deploying, through the API with a fake cluster


def render_or_refuse(knobs: Mapping[str, Any], server: str = "vllm") -> list[str]:
    if "vllm.nope" in knobs:
        raise DeployInputError(['knob "vllm.nope" is not a known vLLM knob'])
    return [f"--max-num-seqs={knobs['vllm.max_num_seqs']}"] if "vllm.max_num_seqs" in knobs else []


@pytest.fixture
def cluster() -> FakeCluster:
    return FakeCluster()


@pytest.fixture
def deploying(
    engine: Engine, data_dir: Path, tmp_path: Path, cluster: FakeCluster
) -> Iterator[TestClient]:
    def service_with_deployer():  # type: ignore[no-untyped-def]
        service = build_service(engine, FakeRunner(), data_dir)
        deployer = Deployer(
            cluster,
            render_or_refuse,
            FakeChecker(check_view("ok")),
            FileSecretStore(tmp_path / "s"),
        )
        service.managed_targets = ManagedTargetService(
            service._repository,  # noqa: SLF001 - the test assembles the service by hand
            TUNING_NAMESPACES,
            deployer,
        )
        return service

    with TestClient(create_app(service_with_deployer)) as test_client:
        yield test_client


def test_deploying_needs_the_kubernetes_deployer(client: TestClient) -> None:
    target = client.post("/managed-targets", json=QWEN).json()
    response = client.post(f"/managed-targets/{target['id']}/deploy", json={"knobs": {}})
    assert response.status_code == 503
    assert client.get(f"/managed-targets/{target['id']}/readiness").json()["state"] == "failed"


def test_deploy_poll_scale_down_and_teardown(deploying: TestClient, cluster: FakeCluster) -> None:
    target = deploying.post("/managed-targets", json=QWEN).json()
    deployed = deploying.post(
        f"/managed-targets/{target['id']}/deploy", json={"knobs": {"vllm.max_num_seqs": 128}}
    )
    assert deployed.status_code == 202, deployed.text
    deployment = deployed.json()["deployment"]
    assert deployment["knobs"] == {"vllm.max_num_seqs": 128}
    assert deployment["args"][-1] == "--max-num-seqs=128"
    assert deploying.get(f"/managed-targets/{target['id']}").json()["deployment"] == deployment

    assert deploying.get(f"/managed-targets/{target['id']}/readiness").json()["state"] == "pending"
    deployment_ref = ObjectRef("apps/v1", "Deployment", NAMESPACE, NAME)
    cluster.objects[deployment_ref]["metadata"]["generation"] = 1
    cluster.objects[deployment_ref]["status"] = {
        "observedGeneration": 1,
        "updatedReplicas": 1,
        "readyReplicas": 1,
    }
    cluster.pod_list = [
        {
            "metadata": {"name": "qwen-1"},
            "spec": {"containers": [{"name": "vllm", "args": deployment["args"]}]},
            "status": {
                "containerStatuses": [{"name": "vllm", "state": {"running": {}}, "ready": True}]
            },
        }
    ]
    ready = deploying.get(f"/managed-targets/{target['id']}/readiness").json()
    assert ready["state"] == "ready"
    assert ready["check"]["result"]["status"] == "ok"

    assert deploying.post(f"/managed-targets/{target['id']}/scale-down").status_code == 204
    assert cluster.scaled == [(NAMESPACE, NAME, 0)]
    scaled = deploying.get(f"/managed-targets/{target['id']}").json()["deployment"]
    assert scaled["scaled_down_at"] is not None, "the list says the GPU is free"
    assert scaled["args"] == deployment["args"]
    redeployed = deploying.post(f"/managed-targets/{target['id']}/deploy", json={"knobs": {}})
    assert "scaled_down_at" not in redeployed.json()["deployment"], "a deploy starts over"
    assert deploying.delete(f"/managed-targets/{target['id']}").status_code == 204
    assert {ref.kind for ref in cluster.deleted} == {"Deployment", "PodMonitor", "Service"}


def test_deploy_refuses_an_unknown_knob_and_a_foreign_object(
    deploying: TestClient, cluster: FakeCluster
) -> None:
    target = deploying.post("/managed-targets", json=QWEN).json()
    unknown = deploying.post(
        f"/managed-targets/{target['id']}/deploy", json={"knobs": {"vllm.nope": 1}}
    )
    assert unknown.status_code == 422
    assert "vllm.nope" in unknown.text
    cluster.objects[ObjectRef("v1", "Service", NAMESPACE, NAME)] = {
        "metadata": {"labels": {"app": "theirs"}}
    }
    foreign = deploying.post(f"/managed-targets/{target['id']}/deploy", json={"knobs": {}})
    assert foreign.status_code == 409
    assert cluster.applied == []


def vllm_pod(name: str, created: str, state: dict[str, Any]) -> dict[str, Any]:
    return {
        "metadata": {"name": name, "creationTimestamp": created},
        "spec": {"containers": [{"name": "vllm", "args": []}]},
        "status": {"containerStatuses": [{"name": "vllm", "state": state, "ready": False}]},
    }


def test_logs_come_from_the_newest_pod(deploying: TestClient, cluster: FakeCluster) -> None:
    """A Recreate rollout can leave an old pod terminating beside the new one."""
    target = deploying.post("/managed-targets", json=QWEN).json()
    logs = f"/managed-targets/{target['id']}/logs"
    assert deploying.get(logs).json() == {
        "source": "no pod: not deployed, or scaled to zero",
        "lines": [],
    }

    cluster.pod_list = [
        vllm_pod("qwen-old", "2026-11-24T09:00:00Z", {"terminated": {"exitCode": 0}}),
        vllm_pod("qwen-new", "2026-11-24T10:00:00Z", {"waiting": {"reason": "ContainerCreating"}}),
    ]
    assert deploying.get(logs).json() == {"source": "pod qwen-new (ContainerCreating)", "lines": []}

    cluster.pod_list[1]["status"]["containerStatuses"][0]["state"] = {
        "waiting": {"reason": "CrashLoopBackOff"}
    }
    cluster.pod_list[1]["status"]["containerStatuses"][0]["lastState"] = {
        "terminated": {"exitCode": 1}
    }
    cluster.logs = {"qwen-new": "ValueError: No available memory for the cache blocks.\n"}
    assert deploying.get(logs).json() == {
        "source": "pod qwen-new",
        "lines": ["ValueError: No available memory for the cache blocks."],
    }

    # SGLang logs its API key at startup; the panel never shows it.
    cluster.logs = {
        "qwen-new": "server_args={'api_key': 'sentinel-secret-api-key', 'port': 8000}\n"
    }
    assert deploying.get(logs).json()["lines"] == [
        "server_args={'api_key': '[redacted]', 'port': 8000}"
    ]


def test_boot_probe_through_the_api(deploying: TestClient, cluster: FakeCluster) -> None:
    target = deploying.post("/managed-targets", json=QWEN).json()
    probe = f"/managed-targets/{target['id']}/boot-probe"
    assert (
        deploying.post(probe, json={"tokens_per_request": 4096}).json()["readiness"]["state"]
        == "failed"
    )
    deployed = deploying.post(
        f"/managed-targets/{target['id']}/deploy", json={"knobs": {"vllm.max_num_seqs": 64}}
    ).json()
    still = deploying.post(probe, json={"tokens_per_request": 4096}).json()
    assert still["readiness"]["state"] == "pending" and "result" not in still
    deployment_ref = ObjectRef("apps/v1", "Deployment", NAMESPACE, NAME)
    cluster.objects[deployment_ref]["metadata"]["generation"] = 1
    cluster.objects[deployment_ref]["status"] = {
        "observedGeneration": 1,
        "updatedReplicas": 1,
        "readyReplicas": 1,
    }
    cluster.pod_list = [
        {
            "metadata": {"name": "qwen-1"},
            "spec": {"containers": [{"name": "vllm", "args": deployed["deployment"]["args"]}]},
            "status": {
                "containerStatuses": [{"name": "vllm", "state": {"running": {}}, "ready": True}]
            },
        }
    ]
    done = deploying.post(probe, json={"tokens_per_request": 4096}).json()
    assert done["result"] == {
        "knobs": {"vllm.max_num_seqs": 64},
        "outcome": "feasible",
        "num_gpu_blocks": 8192,
        "c_kv": 32.0,
    }
    assert deploying.post(probe, json={"tokens_per_request": 0}).status_code == 422
