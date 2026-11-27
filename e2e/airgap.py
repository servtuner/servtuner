"""servtuner installed and used with no internet access.

    PYTHONPATH=api uv run --project api python e2e/airgap.py VERSION [--keep]

Needs `just release VERSION` first (dist/servtuner-VERSION/), plus kind, kubectl, helm, helmfile,
hauler and docker. It makes its own kind cluster (servtuner-airgap) and registry container, and
never touches another cluster:

1. a kind cluster whose containerd reads registry hosts from /etc/containerd/certs.d;
2. a registry container on kind's network, fed by `hauler store load` and `hauler store copy`;
3. the node's egress blocked (iptables in its network namespace), and the block proven: a pull from
   docker.io must fail;
4. `helmfile -e airgap sync` from the release's own deploy bundle, with an Envoy Gateway and the
   mock target (a test fixture pushed to the same registry);
5. the first admin, an uploaded datasheet, two priced mock runs (bench Jobs), a compare and a
   capacity plan, all through the api;
6. every pod image came from the local registry, and no pull failed;

then it deletes what it made (unless --keep).
"""

import argparse
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx2
from common import (
    E2E_ADMIN_PASSWORD,
    E2E_ADMIN_USERNAME,
    REPOSITORY,
    TERMINAL_STATES,
    check,
    stop_process,
    wait_for,
)

CLUSTER = "servtuner-airgap"
CONTEXT = f"kind-{CLUSTER}"
NODE = f"{CLUSTER}-control-plane"
REGISTRY_CONTAINER = "servtuner-airgap-registry"
# What the nodes call the registry: its container name on kind's network.
REGISTRY = f"{REGISTRY_CONTAINER}:5000"
# Containers the test starts carry this label; teardown removes by it.
LABEL = "servtuner.e2e=airgap"
NAMESPACE = "servtuner"
MOCK_NAMESPACE = "servtuner-mock"
# Destinations the node may still reach: loopback, kind's pod and service networks (kind defaults).
POD_SUBNET, SERVICE_SUBNET = "10.244.0.0/16", "10.96.0.0/12"
INSTALL_TIMEOUT_SECONDS = 1200.0
RUN_TIMEOUT_SECONDS = 600.0
# Namespaces whose images kind preloads into its node image; all else must come from REGISTRY.
KIND_NAMESPACES = {"kube-system", "local-path-storage"}

KIND_CONFIG = """\
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.cri.v1.images".registry]
      config_path = "/etc/containerd/certs.d"
nodes:
  - role: control-plane
"""

HOSTS_TOML = f"""\
server = "http://{REGISTRY}"

[host."http://{REGISTRY}"]
  capabilities = ["pull", "resolve"]
"""

SITE_VALUES = {
    "tls": {"enabled": False},
    "runner": "kubernetes",
    "bench": {
        "targetAntiAffinity": "preferred",
        "resources": {"cpu": "1", "memory": "512Mi"},
    },
    "storage": {"file": {"pvc": {"create": True, "accessMode": "ReadWriteOnce", "size": "1Gi"}}},
    "postgres": {"cnpg": {"storage": {"size": "1Gi"}}},
    # httpx sends a Secure cookie only over https; the e2e reaches the api over plain port-forward.
    "auth": {"cookie": {"secure": False}},
    "gateway": {
        "enabled": True,
        "hostname": "servtuner.localtest.me",
        "parentRefs": [{"name": "eg", "namespace": NAMESPACE}],
    },
}

GATEWAY = {
    "apiVersion": "gateway.networking.k8s.io/v1",
    "kind": "Gateway",
    "metadata": {"name": "eg", "namespace": NAMESPACE},
    "spec": {
        "gatewayClassName": "eg",
        "listeners": [
            {
                "name": "http",
                "protocol": "HTTP",
                "port": 80,
                "allowedRoutes": {"namespaces": {"from": "Same"}},
            }
        ],
    },
}

# A datasheet as the operator would upload it (LiteLLM's format), pricing the mock's model.
DATASHEET = {
    "mock-model": {
        "provider": "openai",
        "input_cost_per_token": 1.5e-07,
        "output_cost_per_token": 6e-07,
    }
}


def run(
    *command: str,
    capture: bool = False,
    cwd: Path | None = None,
    check_exit: bool = True,
) -> str:
    print("+", " ".join(command), flush=True)
    result = subprocess.run(command, check=check_exit, cwd=cwd, text=True, capture_output=capture)
    return result.stdout if capture else ""


def kubectl(*arguments: str, capture: bool = False) -> str:
    return run("kubectl", "--context", CONTEXT, *arguments, capture=capture)


def node_shell(script: str, check_exit: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["docker", "exec", NODE, "sh", "-c", script],
        check=check_exit,
        text=True,
        capture_output=True,
    )


# --- 1-3. cluster, registry, no egress ------------------------------------------------------------


def create_cluster(scratch: Path) -> None:
    clusters = run("kind", "get", "clusters", capture=True).split()
    if CLUSTER in clusters:
        raise SystemExit(
            f"airgap: kind cluster {CLUSTER} exists;"
            f" delete it (kind delete cluster --name {CLUSTER})"
        )
    config = scratch / "kind.yaml"
    config.write_text(KIND_CONFIG)
    run(
        "kind",
        "create",
        "cluster",
        "--name",
        CLUSTER,
        "--config",
        str(config),
        "--wait",
        "120s",
    )


def start_registry() -> str:
    """Starts the registry on kind's network; returns where the host reaches it (a port Docker
    picked, on loopback)."""
    run(
        "docker",
        "run",
        "-d",
        "--name",
        REGISTRY_CONTAINER,
        "--label",
        LABEL,
        "--network",
        "kind",
        "-p",
        "127.0.0.1::5000",
        "registry:2",
    )
    host = run("docker", "port", REGISTRY_CONTAINER, "5000/tcp", capture=True).split()[0]
    certs = f"/etc/containerd/certs.d/{REGISTRY}"
    node_shell(f"mkdir -p '{certs}' && cat > '{certs}/hosts.toml' <<'EOF'\n{HOSTS_TOML}EOF")
    return host


def block_egress() -> None:
    subnets = run(
        "docker",
        "network",
        "inspect",
        "kind",
        "-f",
        "{{range .IPAM.Config}}{{.Subnet}} {{end}}",
        capture=True,
    ).split()
    allowed = [
        "127.0.0.0/8",
        POD_SUBNET,
        SERVICE_SUBNET,
        *(net for net in subnets if ":" not in net),
    ]
    rules = [
        "iptables -N SERVTUNER-EGRESS",
        "iptables -A SERVTUNER-EGRESS -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN",
    ]
    rules += [f"iptables -A SERVTUNER-EGRESS -d {net} -j RETURN" for net in allowed]
    rules += [
        "iptables -A SERVTUNER-EGRESS -j REJECT",
        "iptables -I OUTPUT 1 -j SERVTUNER-EGRESS",
        "iptables -I FORWARD 1 -j SERVTUNER-EGRESS",
        # No IPv6 way out either.
        "ip6tables -I OUTPUT 1 ! -o lo -j REJECT || true",
        "ip6tables -I FORWARD 1 -j REJECT || true",
    ]
    node_shell(" && ".join(rules))
    proof = node_shell("crictl pull docker.io/library/busybox:1.37", check_exit=False)
    check(proof.returncode != 0, "egress is blocked: the node cannot pull from docker.io")
    reach = node_shell(
        f"crictl pull {REGISTRY}/jetstack/cert-manager-controller:v1.21.1",
        check_exit=False,
    )
    check(
        reach.returncode == 0,
        f"the node pulls from the local registry ({reach.stderr.strip()[:200]})",
    )


# --- 2, 4. the release into the registry, then installed ------------------------------------------


def load_release(release: Path, scratch: Path, version: str, registry_host: str) -> Path:
    store = scratch / "store"
    run(
        "hauler",
        "store",
        "-s",
        str(store),
        "load",
        "-f",
        str(release / f"servtuner-{version}.tar.zst"),
    )
    run(
        "hauler",
        "store",
        "-s",
        str(store),
        "copy",
        "--plain-http",
        f"registry://{registry_host}",
    )
    unpacked = scratch / "bundle"
    unpacked.mkdir()
    run(
        "hauler",
        "store",
        "-s",
        str(store),
        "extract",
        "-o",
        str(unpacked),
        f"hauler/servtuner-deploy-{version}.tar.gz:latest",
    )
    run(
        "tar",
        "-xzf",
        str(unpacked / f"servtuner-deploy-{version}.tar.gz"),
        "-C",
        str(unpacked),
    )
    return unpacked / "deploy"


def push_mock(registry_host: str) -> None:
    """The mock target is a test fixture, not part of the release: pushed beside it."""
    image = f"{registry_host}/servtuner/servtuner-mock:e2e"
    run("docker", "tag", "servtuner-mock:e2e", image)
    run("docker", "push", image)


def install(deploy: Path) -> None:
    (deploy / "site-values.yaml").write_text(json.dumps(SITE_VALUES))
    run(
        "helmfile",
        "-f",
        str(deploy / "helmfile.yaml.gotmpl"),
        "-e",
        "airgap",
        "--kube-context",
        CONTEXT,
        "--state-values-set",
        f"registry={REGISTRY}",
        "sync",
        "--skip-deps",
    )
    kubectl("apply", "-f", str(deploy / "helmfile" / "envoy-gateway-class.yaml"))
    gateway = deploy / "gateway.json"
    gateway.write_text(json.dumps(GATEWAY))
    kubectl("apply", "-f", str(gateway))
    run(
        "helm",
        "--kube-context",
        CONTEXT,
        "upgrade",
        "--install",
        "openai-mock",
        str(REPOSITORY / "deploy" / "mocks" / "chart"),
        "-n",
        MOCK_NAMESPACE,
        "--create-namespace",
        "--set",
        "tls.enabled=false",
        "--set",
        f"image.repository={REGISTRY}/servtuner/servtuner-mock",
        "--set",
        "image.tag=e2e",
        "--wait",
        "--timeout",
        "300s",
    )
    kubectl(
        "-n",
        NAMESPACE,
        "rollout",
        "status",
        "deployment/servtuner-api",
        "--timeout",
        "600s",
    )
    kubectl(
        "-n",
        "envoy-gateway-system",
        "wait",
        "pod",
        "-l",
        "gateway.envoyproxy.io/owning-gateway-name=eg",
        "--for=condition=Ready",
        "--timeout",
        "300s",
    )
    check(True, "Envoy's proxy for the Gateway runs (its image came from the registry)")


# --- 5. servtuner used offline --------------------------------------------------------------------


def priced_spec(name: str, rates: list[int]) -> dict[str, Any]:
    return {
        "apiVersion": "servtuner.dev/v2",
        "kind": "RunSpec",
        "name": name,
        "target": {
            "type": "openai-compatible",
            "endpoint": f"http://openai-mock.{MOCK_NAMESPACE}.svc:8000/v1",
            "model": "mock-model",
            "tls": {"enabled": False},
            "hardware": {
                "host": "test-node",
                "accelerator": "mock-gpu",
                "gpu_count": 1,
            },
        },
        "engine": {"type": "loadgen"},
        "workload": {
            "api": "chat",
            "streaming": True,
            "dataset": {
                "type": "synthetic",
                "input_tokens": {"mean": 256},
                "output_tokens": {"mean": 32},
            },
        },
        "load": {
            "pacing": "poisson",
            "warmup": "10s",
            "stages": [{"rate": rate, "duration": "30s"} for rate in rates],
        },
        "slos": {"ttft_p95": "500ms"},
        "prometheus": {
            "url": "http://kps-prometheus.monitoring.svc:9090",
            "tls": {"enabled": False},
        },
        "analysis": {
            "infra_pricing": {
                "source": "static",
                "static": {
                    "nodes": {
                        "test-node": {
                            "usd_per_hour": 2.0,
                            "cores": 8,
                            "memory_gib": 32,
                            "gpus": 1,
                        }
                    }
                },
            },
            "token_pricing": {"source": "datasheet", "from_price_book": True},
        },
    }


def finished_run(client: httpx2.Client, run_id: str) -> dict[str, Any]:
    def terminal() -> dict[str, Any] | None:
        found: dict[str, Any] = client.get(f"/runs/{run_id}").json()
        return found if found["status"] in TERMINAL_STATES else None

    found: dict[str, Any] = wait_for(f"run {run_id}", terminal, RUN_TIMEOUT_SECONDS)
    return found


def restart_api() -> None:
    """Restart the api while runs are unfinished: it must pick their Jobs up again. With two
    unfinished runs the restart once crashed it (servtuner_api.kubernetes_decoders)."""
    selector = "app.kubernetes.io/component=api"
    kubectl("-n", NAMESPACE, "delete", "pod", "-l", selector)
    kubectl(
        "-n",
        NAMESPACE,
        "wait",
        "pod",
        "-l",
        selector,
        "--for=condition=Ready",
        "--timeout=300s",
    )


def api_restarts() -> int:
    statuses = kubectl(
        "-n",
        NAMESPACE,
        "get",
        "pod",
        "-l",
        "app.kubernetes.io/component=api",
        "-o",
        "jsonpath={.items[*].status.containerStatuses[*].restartCount}",
        capture=True,
    )
    return sum(int(count) for count in statuses.split())


def use_servtuner(client: httpx2.Client, reconnect: Callable[[], str]) -> None:
    """``reconnect`` restarts the port-forward after the api pod changes; returns its URL."""
    encoded = kubectl(
        "-n",
        NAMESPACE,
        "get",
        "secret",
        "servtuner-setup-token",
        "-o",
        "jsonpath={.data.token}",
        capture=True,
    )
    created = client.post(
        "/auth/setup",
        json={"username": E2E_ADMIN_USERNAME, "password": E2E_ADMIN_PASSWORD},
        headers={"X-Servtuner-Setup-Token": base64.b64decode(encoded).decode()},
    )
    check(created.status_code == 201, f"the first admin signs in ({created.status_code})")
    defaults = client.get("/run-defaults").json()
    check(
        defaults["offline"] is True and defaults["image_registry"] == REGISTRY,
        "the api knows it is offline and where its images come from",
    )
    aws = client.put("/price-book", json={"source": "aws", "aws_region": "us-east-1"})
    check(aws.status_code == 422, "the price book refuses the AWS Price List offline")
    uploaded = client.put("/price-book/datasheet", content=json.dumps(DATASHEET).encode())
    check(
        uploaded.status_code == 200,
        f"the operator uploads a datasheet ({uploaded.status_code})",
    )

    run_ids = []
    for name, rates in (("airgap-low", [5, 10]), ("airgap-high", [10, 20])):
        response = client.post("/runs", content=json.dumps(priced_spec(name, rates)).encode())
        check(response.status_code == 201, f"run {name} created ({response.status_code})")
        run_ids.append(response.json()["id"])
    restart_api()
    client.base_url = httpx2.URL(reconnect())
    wait_for("the restarted api", lambda: client.get("/healthz").is_success, 60.0)
    for run_id in run_ids:
        found = finished_run(client, run_id)
        check(
            found["status"] == "succeeded",
            f"bench ran {found['name']} as a Job ({found['status']})",
        )
        capacity = client.get(f"/runs/{run_id}/capacity").json()
        token = capacity.get("token_cost") or {}
        check(
            token.get("is_priced") is True and token.get("datasheet_source") == "price_book",
            f"{found['name']} is priced by the token from the uploaded datasheet",
        )
        check(
            bool((capacity.get("infra_cost") or {}).get("is_priced")),
            f"{found['name']} has its infrastructure cost",
        )
        environment = client.get(f"/runs/{run_id}/files/environment.json").json()
        check(
            environment.get("offline") is True,
            f"{found['name']} records that it ran offline",
        )

    compared = client.get("/compare", params={"runs": ",".join(run_ids)})
    metrics = {metric["metric"] for metric in compared.json().get("metrics", [])}
    check(
        compared.status_code == 200 and "infra_usd_per_1k_requests" in metrics,
        "the two runs compare, cost per request included",
    )
    request = json.loads(
        (REPOSITORY / "schema/examples/plan-request/valid/minimal.json").read_text()
    )
    plan = client.post("/plans", json={"request": request, "run_ids": run_ids})
    check(
        plan.status_code == 200,
        f"a capacity plan from the two runs ({plan.status_code})",
    )
    restarts = api_restarts()
    check(
        restarts == 0,
        f"the api restarted with two runs in flight and never crashed ({restarts})",
    )


# --- 6. where every image came from ---------------------------------------------------------------


def check_images() -> None:
    pods = json.loads(kubectl("get", "pods", "-A", "-o", "json", capture=True))["items"]
    foreign = sorted(
        {
            f"{pod['metadata']['namespace']}: {container['image']}"
            for pod in pods
            if pod["metadata"]["namespace"] not in KIND_NAMESPACES
            for container in pod["spec"].get("containers", [])
            + pod["spec"].get("initContainers", [])
            if not container["image"].startswith(f"{REGISTRY}/")
        }
    )
    check(not foreign, f"every pod image came from the local registry (else: {foreign})")
    events = json.loads(kubectl("get", "events", "-A", "-o", "json", capture=True))["items"]
    failed = sorted(
        {
            event["message"][:160]
            for event in events
            if event.get("reason") in {"Failed", "ErrImagePull"}
        }
    )
    check(not failed, f"no image pull failed (else: {failed})")


def teardown() -> None:
    run("kind", "delete", "cluster", "--name", CLUSTER, check_exit=False)
    labelled = run("docker", "ps", "-aq", "--filter", f"label={LABEL}", capture=True).split()
    if labelled:
        run("docker", "rm", "-f", *labelled, check_exit=False)


def port_forward_api(scratch: Path) -> tuple[subprocess.Popen[bytes], str]:
    """kubectl port-forward on a local port it picks; returns the process and the api's URL. Its
    output goes to a file: kubectl logs every connection, and an unread pipe would block it.
    """
    log = scratch / "port-forward.log"
    with log.open("wb") as output:
        forward = subprocess.Popen(
            [
                "kubectl",
                "--context",
                CONTEXT,
                "-n",
                NAMESPACE,
                "port-forward",
                "svc/servtuner-api",
                ":8080",
            ],
            stdout=output,
        )

    def address() -> str | None:
        # "Forwarding from 127.0.0.1:<port> -> 8080"
        for line in log.read_text().splitlines():
            if line.startswith("Forwarding from 127.0.0.1:"):
                return line.split("from ", 1)[1].split(" ->", 1)[0]
        return None

    found: str = wait_for("kubectl port-forward to listen", address, 30.0)
    return forward, f"http://{found}"


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    parser.add_argument("version")
    parser.add_argument(
        "--keep", action="store_true", help="leave the cluster and registry running"
    )
    arguments = parser.parse_args()
    release = REPOSITORY / "dist" / f"servtuner-{arguments.version}"
    if not (release / "SHA256SUMS").exists():
        raise SystemExit(
            f"airgap: no release at {release}: run `just release {arguments.version}` first"
        )
    for tool in ("kind", "kubectl", "helm", "helmfile", "hauler", "docker"):
        if shutil.which(tool) is None:
            raise SystemExit(f"airgap: {tool} is not on PATH")
    run("sha256sum", "-c", "SHA256SUMS", cwd=release)
    forward = None
    with tempfile.TemporaryDirectory(prefix="servtuner-airgap-") as directory:
        scratch = Path(directory)
        # Its own kubeconfig: kind, kubectl, helm and helmfile all read KUBECONFIG, so the user's
        # ~/.kube/config and current context are never touched (kind create switches the context).
        os.environ["KUBECONFIG"] = str(scratch / "kubeconfig")
        try:
            create_cluster(scratch)
            registry_host = start_registry()
            deploy = load_release(release, scratch, arguments.version, registry_host)
            push_mock(registry_host)
            block_egress()
            install(deploy)
            forward, api_url = port_forward_api(scratch)

            def reconnect() -> str:
                nonlocal forward
                if forward is not None:
                    stop_process(forward)
                forward, url = port_forward_api(scratch)
                return url

            with httpx2.Client(base_url=api_url, timeout=60.0) as client:
                wait_for(
                    "the api through port-forward",
                    lambda: client.get("/healthz").is_success,
                    60.0,
                )
                use_servtuner(client, reconnect)
            check_images()
            print("airgap: passed", flush=True)
        finally:
            if forward is not None:
                stop_process(forward)
            if not arguments.keep:
                teardown()
    return 0


if __name__ == "__main__":
    sys.exit(main())
