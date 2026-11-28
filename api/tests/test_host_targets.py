"""Host targets: a vLLM outside Kubernetes run by ``servtuner launcher``.

The launcher is faked with an httpx2 MockTransport that answers as bench's launcher does
(bench/internal/launcher), so the deployer's requests and its readings of the answers are tested.
"""

import copy
import dataclasses
import json
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import httpx2
import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.deployer.base import DeployInputError
from servtuner_api.deployer.launcher import LauncherDeployer, LauncherError
from servtuner_api.managed_targets.runs import run_target
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.managed_targets.spec import HostTargetSpec
from servtuner_api.secret_store import FileSecretStore
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.managed import FakeChecker, check_view

TOKEN = "launcher-token-0123456789"
HOST: dict[str, Any] = {
    "name": "rtx3070",
    "namespace": "servtuner-external",
    "spec": {
        "placement": "host",
        "launcher_url": "http://vllm-host.example:9123",
        "launcher_token": {"name": "launcher-token", "key": "token"},
        "endpoint": "http://vllm-host.example:8080/v1",
        "model": "Qwen/Qwen2.5-0.5B-Instruct",
        "served_model_name": "tiny",
        "base_flags": ["--max-model-len=4096"],
        "auth": {"name": "vllm-key", "key": "token"},
        "metrics": {"job": "servtuner-external", "pod": "vllm-lan", "node": "vllm-lan-gpu"},
    },
}
SPEC = HostTargetSpec.model_validate(HOST["spec"])
# GET /machine as servtuner launcher answers it (schema/examples/host-machine/valid/rtx3070.json).
MACHINE: dict[str, Any] = json.loads(
    (Path(__file__).parents[2] / "schema/examples/host-machine/valid/rtx3070.json").read_text()
)


class FakeLauncher:
    """Answers as servtuner launcher does, and records what it was asked."""

    def __init__(self) -> None:
        self.status: dict[str, Any] = {"state": "none"}
        self.log = ""
        self.version: str | None = "0.34.0"
        self.requests: list[tuple[str, str, Any]] = []
        self.log_tails: list[str | None] = []
        self.refuse: str | None = None
        self.machine: dict[str, Any] | None = MACHINE

    def handle(self, request: httpx2.Request) -> httpx2.Response:
        if request.headers.get("Authorization") != f"Bearer {TOKEN}":
            return httpx2.Response(401, json={"error": "bad token"})
        body = json.loads(request.content) if request.content else None
        self.requests.append((request.method, request.url.path, body))
        self.log_tails.append(request.url.params.get("tail"))
        if request.method == "PUT":
            if self.refuse:
                return httpx2.Response(400, json={"error": self.refuse})
            assert body is not None
            self.status = {"state": "running", "pid": 42, "args": body["args"]}
            return httpx2.Response(200, json=self.status)
        if request.method == "DELETE":
            self.status = {**self.status, "state": "stopped"}
            return httpx2.Response(200, json=self.status)
        if request.url.path == "/machine":
            if self.machine is None:  # a launcher older than the endpoint
                return httpx2.Response(404, text="404 page not found")
            return httpx2.Response(200, json=self.machine)
        if request.url.path == "/vllm/log":
            return httpx2.Response(200, text=self.log)
        if request.url.path == "/vllm/version":
            if self.version is None:  # a launcher older than the endpoint
                return httpx2.Response(404, text="404 page not found")
            return httpx2.Response(200, json={"version": self.version})
        return httpx2.Response(200, json=self.status)


def deployer(tmp_path: Path, launcher: FakeLauncher, view: Any = None) -> LauncherDeployer:
    store = FileSecretStore(tmp_path / "store")
    store.put("launcher-token", {"token": TOKEN}, "target")
    return LauncherDeployer(
        render_knobs=lambda knobs, server: (
            [f"--max-num-seqs={knobs['vllm.max_num_seqs']}"] if knobs else []
        ),
        checker=FakeChecker(view or check_view("ok")),
        store=store,
        prometheus={
            "url": "http://kps-prometheus.monitoring.svc:9090",
            "tls": {"enabled": False},
        },
        transport=httpx2.MockTransport(launcher.handle),
    )


def test_the_run_target_selects_the_hosts_series_and_names_its_key() -> None:
    assert run_target("rtx3070", "servtuner-external", SPEC) == {
        "type": "vllm",
        "endpoint": "http://vllm-host.example:8080/v1",
        "model": "tiny",
        "metrics": {
            "job": "servtuner-external",
            "namespace": "servtuner-external",
            "pod": "vllm-lan",
            "node": "vllm-lan-gpu",
        },
        "tls": {"enabled": False},
        "auth": {"secretRef": {"name": "vllm-key", "key": "token"}},
    }


def test_deploy_starts_vllm_with_the_rendered_args(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    args = deployer(tmp_path, launcher).deploy(
        "rtx3070", "servtuner-external", SPEC, {"vllm.max_num_seqs": 64}
    )
    assert args == [
        "Qwen/Qwen2.5-0.5B-Instruct",
        "--host=0.0.0.0",
        "--port=8000",
        "--served-model-name=tiny",
        "--max-model-len=4096",
        "--max-num-seqs=64",
    ]
    assert launcher.requests == [("PUT", "/vllm", {"args": args, "env": {}})]


def test_an_offline_install_never_lets_flashinfer_download_on_a_host(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    offline = dataclasses.replace(deployer(tmp_path, launcher), offline=True)
    args = offline.deploy("rtx3070", "servtuner-external", SPEC, {})
    assert launcher.requests == [
        ("PUT", "/vllm", {"args": args, "env": {"FLASHINFER_NO_DOWNLOAD": "1"}})
    ]


def test_poll_reads_the_launcher(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    deployed = deployer(tmp_path, launcher)
    assert (
        deployed.poll("t", "rtx3070", "servtuner-external", SPEC, []).state == "failed"
    )  # nothing started
    args = deployed.deploy("rtx3070", "servtuner-external", SPEC, {"vllm.max_num_seqs": 64})
    ready = deployed.poll("t", "rtx3070", "servtuner-external", SPEC, args)
    assert ready.state == "ready"
    assert deployed.checker.requests[-1]["target"]["metrics"]["pod"] == "vllm-lan"  # type: ignore[attr-defined]
    assert (
        "other args" in deployed.poll("t", "rtx3070", "servtuner-external", SPEC, args[:-1]).reason
    )
    launcher.status = {"state": "exited", "exit_code": 1, "args": args}
    launcher.log = (
        "loading\nValueError: Free memory on device cuda:0 (5.1/8.0 GiB) on startup is less "
        "than desired GPU memory utilization (0.9, 7.2 GiB)."
    )
    infeasible = deployed.poll("t", "rtx3070", "servtuner-external", SPEC, args)
    assert (infeasible.state, infeasible.failure.rule) == ("infeasible", "not-enough-free-memory")  # type: ignore[union-attr]
    assert ("GET", "/vllm/log", None) in launcher.requests


def test_metrics_not_yet_visible_keep_it_pending(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    deployed = deployer(tmp_path, launcher, check_view("failed", failed="metrics"))
    args = deployed.deploy("rtx3070", "servtuner-external", SPEC, {})
    assert deployed.poll("t", "rtx3070", "servtuner-external", SPEC, args).state == "pending"


def test_scale_to_zero_stops_vllm(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    deployed = deployer(tmp_path, launcher)
    deployed.deploy("rtx3070", "servtuner-external", SPEC, {})
    deployed.scale_to_zero("rtx3070", "servtuner-external", SPEC)
    assert launcher.requests[-1][:2] == ("DELETE", "/vllm")


def test_refusals_and_failures(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    launcher.refuse = "HF_TOKEN is a secret: the launcher reads it on the host"
    with pytest.raises(DeployInputError, match="refused"):
        deployer(tmp_path, launcher).deploy("rtx3070", "servtuner-external", SPEC, {})
    no_token = deployer(tmp_path, launcher)
    no_token.store = FileSecretStore(tmp_path / "empty")
    with pytest.raises(DeployInputError, match="launcher_token"):
        no_token.deploy("rtx3070", "servtuner-external", SPEC, {})

    def unreachable(request: httpx2.Request) -> httpx2.Response:
        raise httpx2.ConnectError("connection refused", request=request)

    down = deployer(tmp_path, launcher)
    down.transport = httpx2.MockTransport(unreachable)
    with pytest.raises(LauncherError, match="unreachable"):
        down.poll("t", "rtx3070", "servtuner-external", SPEC, [])


# --- through the api


@pytest.fixture
def client(
    engine: Engine, data_dir: Path, tmp_path: Path
) -> Iterator[tuple[TestClient, FakeLauncher]]:
    launcher = FakeLauncher()

    def service_with_hosts():  # type: ignore[no-untyped-def]
        service = build_service(engine, FakeRunner(), data_dir)
        host_deployer = deployer(tmp_path, launcher)
        service.managed_targets = ManagedTargetService(
            service._repository,  # noqa: SLF001 - the test assembles the service by hand
            (),  # no tuning namespace: host targets need none
            host_deployer=host_deployer,
            secret_store=host_deployer.store,
        )
        return service

    with TestClient(create_app(service_with_hosts)) as test_client:
        yield test_client, launcher


def test_a_host_target_through_the_api(client: tuple[TestClient, FakeLauncher]) -> None:
    api, launcher = client
    created = api.post("/managed-targets", json=HOST)
    assert created.status_code == 201, created.text
    target = created.json()
    assert target["run_target"]["metrics"]["job"] == "servtuner-external"
    deployed = api.post(
        f"/managed-targets/{target['id']}/deploy", json={"knobs": {"vllm.max_num_seqs": 32}}
    )
    assert deployed.status_code == 202, deployed.text
    assert api.get(f"/managed-targets/{target['id']}/readiness").json()["state"] == "ready"
    assert api.post(f"/managed-targets/{target['id']}/scale-down").status_code == 204
    assert api.delete(f"/managed-targets/{target['id']}").status_code == 204
    assert [request[0] for request in launcher.requests] == ["PUT", "GET", "DELETE", "DELETE"]


def test_a_host_targets_log_comes_from_its_launcher(
    client: tuple[TestClient, FakeLauncher],
) -> None:
    api, launcher = client
    target = api.post("/managed-targets", json=HOST).json()
    launcher.log = "INFO starting\nINFO Uvicorn running\n"
    response = api.get(f"/managed-targets/{target['id']}/logs", params={"tail_lines": 50})
    assert response.status_code == 200, response.text
    assert response.json() == {
        "source": f"launcher {HOST['spec']['launcher_url']}",
        "lines": ["INFO starting", "INFO Uvicorn running"],
    }
    assert launcher.log_tails[-1] == "50"
    too_many = api.get(f"/managed-targets/{target['id']}/logs", params={"tail_lines": 1001})
    assert too_many.status_code == 422


def test_pasted_credentials_are_kept_write_only(
    client: tuple[TestClient, FakeLauncher], tmp_path: Path
) -> None:
    """The form pastes the launcher token and vLLM's key; servtuner keeps them in its own
    Secrets."""
    api, launcher = client
    without = copy.deepcopy(HOST)
    del without["spec"]["launcher_token"], without["spec"]["auth"]
    target = api.post("/managed-targets", json=without).json()
    route = f"/managed-targets/{target['id']}"
    assert target["credentials"] == {
        "launcher_token": {"is_set": False},
        "api_key": {"is_set": False},
    }
    refused = api.post(f"{route}/deploy", json={"knobs": {}})
    assert refused.status_code == 422 and "launcher token" in refused.text

    for slot, value in (("launcher_token", TOKEN), ("api_key", "vllm-key-value")):
        response = api.put(f"{route}/credentials/{slot}", json={"value": value})
        assert response.status_code == 204 and response.content == b""
    saved = api.get(route).json()
    assert value not in json.dumps(saved) and TOKEN not in json.dumps(saved)
    token_secret = f"servtuner-managed-{target['id'].lower()}-launcher-token"
    key_secret = f"servtuner-managed-{target['id'].lower()}-api-key"
    assert saved["credentials"]["launcher_token"] == {
        "is_set": True,
        "source": "managed",
        "secret_name": token_secret,
    }
    assert saved["run_target"]["auth"] == {"secretRef": {"name": key_secret, "key": "token"}}
    assert api.post(f"{route}/deploy", json={"knobs": {}}).status_code == 202
    assert launcher.requests[-1][0] == "PUT", "the pasted token opened the launcher"

    # A Secret the user names replaces servtuner's, which is deleted; theirs is never deleted.
    store = FileSecretStore(tmp_path / "store")
    store.put("vllm-key", {"token": "theirs"}, "user")
    external = api.put(f"{route}/credentials/api_key", json={"external": {"name": "vllm-key"}})
    assert external.status_code == 204
    assert api.get(route).json()["credentials"]["api_key"]["source"] == "external"
    assert store.read(key_secret) is None
    assert api.delete(f"{route}/credentials/api_key").status_code == 204
    assert "auth" not in api.get(route).json()["run_target"]
    assert store.read("vllm-key") == {"token": "theirs"}, "a Secret servtuner did not make stays"

    assert api.delete(route).status_code == 204
    assert store.read(token_secret) is None, "deleting the target deletes servtuner's Secrets"


def test_check_launcher_explains_what_is_wrong(
    client: tuple[TestClient, FakeLauncher], tmp_path: Path
) -> None:
    api, launcher = client
    target = api.post("/managed-targets", json=HOST).json()
    check = f"/managed-targets/{target['id']}/check-launcher"
    launcher.status = {"state": "running", "args": ["Qwen/Qwen2.5-0.5B-Instruct"]}
    assert api.post(check).json() == {
        "status": "ok",
        "message": "reached, token accepted: running vLLM",
        "vllm_state": "running",
        "vllm_args": ["Qwen/Qwen2.5-0.5B-Instruct"],
    }
    FileSecretStore(tmp_path / "store").put("launcher-token", {"token": "wrong"}, "target")
    assert api.post(check).json()["status"] == "refused"
    without = copy.deepcopy(HOST) | {"name": "no-token", "namespace": "servtuner-external-2"}
    del without["spec"]["launcher_token"]
    tokenless = api.post("/managed-targets", json=without).json()
    no_token = api.post(f"/managed-targets/{tokenless['id']}/check-launcher").json()
    assert no_token["status"] == "no_token" and "launcher token" in no_token["message"]


def test_the_launcher_reports_the_installed_vllm_version(tmp_path: Path) -> None:
    launcher = FakeLauncher()
    assert deployer(tmp_path, launcher).installed_vllm_version(SPEC) == "0.34.0"
    launcher.version = None
    assert deployer(tmp_path, launcher).installed_vllm_version(SPEC) is None


def test_an_unreachable_launcher_is_named(tmp_path: Path) -> None:
    def refuse(request: httpx2.Request) -> httpx2.Response:
        raise httpx2.ConnectError("connection refused", request=request)

    unreachable = deployer(tmp_path, FakeLauncher())
    unreachable.transport = httpx2.MockTransport(refuse)
    result = unreachable.check(SPEC)
    assert result.status == "unreachable" and "http://vllm-host.example:9123" in result.message


def test_unknown_targets_and_slots_are_refused(client: tuple[TestClient, FakeLauncher]) -> None:
    api, _ = client
    bad = api.put(
        "/managed-targets/01JNOTATARGET0000000000000/credentials/api_key", json={"value": "x"}
    )
    assert bad.status_code == 404
    target = api.post("/managed-targets", json=HOST).json()
    unknown = api.put(f"/managed-targets/{target['id']}/credentials/admin", json={"value": "x"})
    assert unknown.status_code == 422 and "launcher_token, api_key" in unknown.text


@pytest.mark.parametrize(
    ("change", "reason"),
    [
        ({"endpoint": "https://vllm-host.example:8443/v1"}, "endpoint"),
        ({"base_flags": ["--port=9000"]}, "servtuner sets --port"),
        ({"env": {"HF_TOKEN": "x"}}, "HF_TOKEN"),
        ({"launcher_url": "ftp://x"}, "launcher_url"),
    ],
)
def test_host_targets_refuse_what_they_cannot_run(
    client: tuple[TestClient, FakeLauncher], change: dict[str, Any], reason: str
) -> None:
    body = copy.deepcopy(HOST)
    body["spec"].update(change)
    response = client[0].post("/managed-targets", json=body)
    assert response.status_code == 422
    assert reason in response.text


def test_a_host_record_is_priced_read_from_its_launcher_and_kept_while_used(
    client: tuple[TestClient, FakeLauncher],
) -> None:
    api, launcher = client
    owned = {
        "kind": "owned",
        "purchase_usd": 500,
        "amortization_hours": 8760,
        "watts": 220,
        "usd_per_kwh": 0.087,
    }
    created = api.post("/hosts", json={"name": "gpu-box", "price": owned})
    assert created.status_code == 201, created.text
    host = created.json()
    # The owned RTX 3070 of bench's pricing test (pricing_test.go), by the same formula.
    assert host["usd_per_hour"] == pytest.approx(500 / 8760 + 220 / 1000 * 0.087)
    assert host["price_basis"].startswith("500 USD ÷ 8760 h + 220 W ÷ 1000 × 0.087 USD/kWh")
    assert host["hardware"] == {"gpus": [], "source": "manual"}
    assert api.post("/hosts", json={"name": "gpu-box"}).status_code == 409

    route = f"/hosts/{host['id']}"
    nobody = api.post(f"{route}/read-machine")
    assert nobody.status_code == 422, "no target on it, so no launcher to read it"

    target = api.post(
        "/managed-targets", json={**HOST, "spec": {**HOST["spec"], "host_id": host["id"]}}
    ).json()
    assert api.get(route).json()["target_ids"] == [target["id"]]
    read = api.post(f"{route}/read-machine")
    assert read.status_code == 200, read.text
    hardware = read.json()["hardware"]
    assert hardware["source"] == "launcher"
    assert hardware["gpus"][0]["model"] == "NVIDIA GeForce RTX 3070"
    assert hardware["cpu_cores"] == MACHINE["cpu_cores"]
    assert ("GET", "/machine", None) in launcher.requests

    # Its runs carry the host's hardware and its price shared over its GPUs.
    resource = api.get(f"/managed-targets/{target['id']}").json()
    assert resource["run_target"]["hardware"] == {
        "host": "gpu-box",
        "accelerator": "NVIDIA GeForce RTX 3070",
        "gpu_count": 1,
        "gpu_memory_bytes": MACHINE["gpus"][0]["memory_bytes"],
        "driver_version": MACHINE["gpus"][0]["driver_version"],
        "cpu_model": MACHINE["cpu_model"],
        "cpu_cores": MACHINE["cpu_cores"],
    }
    # A priced host with known hardware can rank by GPU and cost objectives.
    assert resource["session_objectives"] == [
        "slo_max_rps",
        "p95_ttft_at_target_rps",
        "slo_rps_per_gpu",
        "slo_rps_per_replica",
        "tokens_per_s_per_gpu",
        "infra_cost_per_1k_requests",
        "infra_cost_per_1m_output_tokens",
    ]
    assert resource["run_analysis"] == {
        "infra_pricing": {
            "source": "static",
            "static": {
                "gpus": {"NVIDIA GeForce RTX 3070": {k: v for k, v in owned.items() if k != "kind"}}
            },
        }
    }

    launcher.machine = None
    old = api.post(f"{route}/read-machine")
    assert old.status_code == 422
    assert "too old" in old.text

    in_use = api.delete(route)
    assert in_use.status_code == 409
    assert HOST["name"] in in_use.json()["detail"]
    assert api.delete(f"/managed-targets/{target['id']}").status_code == 204
    assert api.delete(route).status_code == 204
    assert api.get(route).status_code == 404


def test_a_host_target_names_a_host_that_exists(client: tuple[TestClient, FakeLauncher]) -> None:
    api, _ = client
    # The install's price book prices Kubernetes nodes, never a host.
    book = {"source": "static", "gpus": {"NVIDIA GeForce RTX 3070": {"usd_per_hour": 0.1}}}
    assert api.put("/price-book", json=book).status_code == 200
    bare = api.post("/managed-targets", json=HOST).json()
    assert bare["session_objectives"] == ["slo_max_rps", "p95_ttft_at_target_rps"]
    assert bare.get("run_analysis") is None
    assert api.delete(f"/managed-targets/{bare['id']}").status_code == 204
    missing = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
    rejected = api.post(
        "/managed-targets", json={**HOST, "spec": {**HOST["spec"], "host_id": missing}}
    )
    assert rejected.status_code == 422
    assert f"no host {missing}" in rejected.text


def test_a_cpu_vllm_ranks_by_slo_max_rps_and_never_per_gpu() -> None:
    from servtuner_api.managed_targets.runs import session_objectives
    from servtuner_api.managed_targets.spec import ManagedTargetSpec

    base = {
        "placement": "kubernetes",
        "image": "vllm/vllm-openai:v0.34.0",
        "model": "Qwen/Qwen2.5-0.5B-Instruct",
        "cpu": "4",
        "memory": "8Gi",
        "model_cache": {"size": "10Gi"},
    }
    cpu = session_objectives(ManagedTargetSpec.model_validate({**base, "gpus": 0}), None, False)
    assert cpu[0] == "slo_max_rps"
    assert "slo_rps_per_gpu" not in cpu and "tokens_per_s_per_gpu" not in cpu
    gpu = session_objectives(ManagedTargetSpec.model_validate({**base, "gpus": 1}), None, False)
    assert gpu[0] == "slo_rps_per_gpu"
