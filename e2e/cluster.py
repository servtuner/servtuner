"""Cluster end to end: the chart on a Kubernetes cluster, in one transport mode.

    PYTHONPATH=api uv run --project api python e2e/cluster.py mutual-tls|tls-off --image-tag TAG
        [--runner subprocess|kubernetes] [--load] [--keep]

Builds the api and frontend images, loads them into the kind cluster, installs the chart into a
fresh namespace ``servtuner-e2e-<mode>`` and drives it through the ingress: a run is created through
the api, polled to ``succeeded``, its bundle is found in the api pod, and the UI is shown to list
it. With ``mutual-tls`` it also shows that the api refuses a connection without a client
certificate.
With ``--runner kubernetes`` bench runs as a Job (namespace suffix ``-jobs``), and the run must be
seen to have been a Job that completed.
The local counterpart (no cluster) is ``e2e/local.py``; the run exercise is shared in ``common.py``.

Safety, because the cluster may be shared:
- It refuses to run unless the current kube context equals ``SERVTUNER_KUBE_CONTEXT`` (default
  ``kind-kind``), and passes that context to every kubectl, helm and kind call.
- It applies no manifest itself, only `kubectl create namespace` and `helm install`. The chart,
  through helm, creates the cluster-scoped ClusterRole and ClusterRoleBinding
  ``<release>-bench-nodes``; cleanup checks that they are gone after `helm uninstall` and deletes
  exactly those two names if not. The ``servtuner-ca`` ClusterIssuer (mutual-tls) must already
  exist:
  apply deploy/kind/cluster-issuer.yaml yourself.
- Every run builds images under its own tag (``<--image-tag>-<nonce>``) and removes exactly those
  tags from every kind node and from local docker on exit.
- Everything it started is torn down on exit, including on failure and on SIGINT, SIGTERM and
  SIGHUP, unless ``--keep`` is given.
"""

import argparse
import base64
import json
import os
import re
import secrets
import shlex
import signal
import subprocess
import sys
import time
from contextlib import ExitStack
from dataclasses import dataclass, field
from functools import partial
from pathlib import Path
from types import FrameType
from urllib.parse import urljoin, urlparse

import httpx2
from common import (
    EXAMPLE_FOR_MODE,
    REPOSITORY,
    Security,
    check,
    check_manifest_succeeded,
    client_context,
    exercise_run,
    refuse_without_client_certificate,
    scratch_directory,
    stop_process,
    wait_for,
)
from forward_header import (
    ADMIN_SUBJECT,
    STUB_NAME,
    as_subject,
    exercise_forward_header,
    probe_network_policy,
    stub_manifests,
    write_chart_values,
)
from tuning import (
    exercise_managed_ui,
    exercise_tuning,
    exercise_tuning_host,
    host_ui_target,
    kubernetes_ui_target,
)

from servtuner_api.datadir import run_bundle_dir
from tests.support.certs import LOOPBACK
from tests.support.specs import example_bytes

CONTEXT_ENV = "SERVTUNER_KUBE_CONTEXT"
HOST_ENV = "SERVTUNER_E2E_HOST"
DEFAULT_CONTEXT = "kind-kind"
KIND_CONTEXT_PREFIX = "kind-"
CHART = REPOSITORY / "deploy" / "helm" / "servtuner"
E2E_VALUES = REPOSITORY / "deploy" / "kind" / "e2e-values.yaml"
LOAD_VALUES = REPOSITORY / "deploy" / "kind" / "e2e-load-values.yaml"
JOBS_VALUES = REPOSITORY / "deploy" / "kind" / "e2e-jobs-values.yaml"
# A tuning session against the mock as a managed vLLM (e2e/tuning.py).
TUNING_VALUES = REPOSITORY / "deploy" / "kind" / "e2e-tuning-values.yaml"
TUNING_IMAGE_NAME = "servtuner-mock"
TUNING_IMAGE_RECIPE = "_image-mock"
# --tuning-host: a real GPU host running `servtuner launcher`. Addresses and secrets come
# from the environment only, never from a committed file (deploy/CLAUDE.md "Constraints").
HOST_ENV_VARS = (
    "SERVTUNER_E2E_LAUNCHER_URL",  # http://<host>:9123
    "SERVTUNER_E2E_VLLM_ENDPOINT",  # http://<host>:8080/v1, as clients reach vLLM
    "SERVTUNER_E2E_LAUNCHER_TOKEN",  # the launcher's bearer token
    "SERVTUNER_E2E_VLLM_KEY",  # vLLM's API key, for the runs' target.auth
)
LAUNCHER_TOKEN_SECRET = "servtuner-launcher-token"
VLLM_KEY_SECRET = "servtuner-host-vllm-key"
RUNNERS = ("subprocess", "kubernetes")
# The label the Kubernetes runner puts on a run's Job and pod (api kubernetes_runner.RUN_ID_LABEL).
RUN_ID_LABEL = "servtuner.dev/run-id"
# The client-certificate Secret the mutual-tls example RunSpec names for its target
# (schema/examples/runspec/valid/minimal-vllm-client-cert.json).
EXAMPLE_TARGET_CLIENT_SECRET = "servtuner-client"
# Real load against the mock, and against servtuner's Bifrost routing to the mock.
LOAD_RUNSPECS = (
    REPOSITORY / "deploy" / "mocks" / "examples" / "mock-direct-tls-off.json",
    REPOSITORY / "deploy" / "mocks" / "examples" / "bifrost-to-mock.json",
)
# Each engine's ceiling against the mock.
CALIBRATION_RUNSPECS = (REPOSITORY / "deploy" / "kind" / "calibration" / "loadgen-ladder.json",)
# Warmup and stages (about 5.5 min: 150s stages are 10 x the 15s servtuner-cadvisor interval),
# the settle delay, collection and analysis, with margin.
LOAD_RUN_TIMEOUT_SECONDS = 1200.0
# Prerequisites of --load that this script never installs (deploy/mocks/README.md).
LOAD_TARGET_SERVICES = (
    ("servtuner-mock", "openai-mock"),
    ("servtuner-bifrost", "servtuner-bifrost"),
)
CLUSTER_ISSUER = "servtuner-ca"
CLUSTER_ISSUER_FILE = "deploy/kind/cluster-issuer.yaml"
INGRESS_NAMESPACE = "ingress-nginx"
INGRESS_SERVICE = "ingress-nginx-controller"
INGRESS_HTTP_PORT = 80
# servtuner.fullname truncates to this (deploy/helm/servtuner/templates/_helpers.tpl).
CHART_NAME_LENGTH = 40
# Envoy Gateway (deploy/kind/envoy-gateway-class.yaml); the proxy runs in its namespace.
GATEWAY_CLASS = "eg"
GATEWAY_NAME = "servtuner-e2e"
GATEWAY_NAMESPACE = "envoy-gateway-system"
# A non-root context path, so the chart's base-path handling is exercised (frontend/CLAUDE.md).
UI_BASE_PATH = "/servtuner/"
# Loaded into kind: what the chart deploys.
IMAGE_NAMES = ("servtuner-api", "servtuner-frontend")
# Built locally but never loaded: the api image takes bench from the bench image (api/Dockerfile).
BUILD_ONLY_IMAGE_NAMES = ("servtuner-bench",)
# kind loads images into containerd under this registry prefix.
CONTAINERD_IMAGE_PREFIX = "docker.io/library/"
CLUSTER_SCOPED_KINDS = ("clusterrolebinding", "clusterrole")
CLUSTER_SCOPED_SUFFIX = "-bench-nodes"
IMAGE_RECIPES = ("_image-api", "_image-frontend")
WAIT_TIMEOUT = "600s"
ROUTE_TIMEOUT_SECONDS = 120.0
PORT_FORWARD_TIMEOUT_SECONDS = 30.0
CLEANUP_TIMEOUT = "300s"
STEP_COUNT = 8
FORWARDING_LINE = re.compile(rf"Forwarding from {re.escape(LOOPBACK)}:(\d+) ->")


class E2EError(Exception):
    """A precondition of the end to end is not met; the message says what to do."""


@dataclass(frozen=True)
class Deployment:
    """One install of the chart. The release is named like the namespace, so the chart's
    resource names (``<release>-api``, ``<release>-ingress-tls``, ...) are all derived from it.
    """

    context: str
    mode: Security
    image_tag: str
    keep: bool
    # Real load runs instead of the null engine (--load).
    load: bool = False
    # Calibration ladders instead of the load runs (--calibrate, implies the load install).
    calibrate: bool = False
    # Who runs bench: the api pod itself, or a Job per run (--runner).
    runner: str = "subprocess"
    # RunSpecs to run instead of LOAD_RUNSPECS with --load (--runspec).
    runspecs: tuple[Path, ...] = ()
    # A tuning session on the mock as a managed vLLM (--tuning; Job runner, tls-off).
    tuning: bool = False
    # A tuning session on a real GPU host through its launcher (--tuning-host).
    tuning_host: bool = False
    # With --tuning: drive the managed vLLM from the UI with Playwright instead of the api (--ui).
    ui: bool = False
    # Forward-header auth behind the ingress or gateway, and a stub auth service.
    forward_header: bool = False
    # Route through Envoy Gateway (Gateway API HTTPRoutes) instead of ingress-nginx.
    gateway: bool = False
    # Cleanup that did not fully work; reported loudly at the end and fails the run.
    problems: list[str] = field(default_factory=list)

    @property
    def namespace(self) -> str:
        suffix = ("-load" if self.load else "") + ("-jobs" if self.runs_jobs else "")
        suffix += "-tuning" if self.tuning else ""
        suffix += "-ui" if self.ui else ""
        suffix += "-tuning-host" if self.tuning_host else ""
        suffix += "-fwd" if self.forward_header else ""
        suffix += "-gw" if self.gateway else ""
        namespace = f"servtuner-e2e-{self.mode.value}{suffix}"
        # The release is named like the namespace and the chart cuts its names to this length
        # (servtuner.fullname), so a longer one would name resources unlike the release.
        if len(namespace) > CHART_NAME_LENGTH:
            raise E2EError(f"namespace {namespace} is longer than {CHART_NAME_LENGTH} characters")
        return namespace

    @property
    def target_namespace(self) -> str:
        """Where the managed vLLM goes (--tuning): the chart's one tuning namespace."""
        return f"{self.namespace}-target"

    @property
    def runs_jobs(self) -> bool:
        return self.runner == "kubernetes"

    @property
    def release(self) -> str:
        return self.namespace

    @property
    def host(self) -> str:
        return os.environ.get(HOST_ENV) or f"{self.namespace}.localtest.me"

    @property
    def images(self) -> list[str]:
        """The images loaded into kind; bench's too when it runs as a Job."""
        names = (*IMAGE_NAMES, *BUILD_ONLY_IMAGE_NAMES) if self.runs_jobs else IMAGE_NAMES
        if self.tuning:
            names = (*names, TUNING_IMAGE_NAME)
        return [f"{name}:{self.image_tag}" for name in names]

    @property
    def built_images(self) -> list[str]:
        """Every local tag this run builds, so local cleanup removes all of them."""
        extra = (TUNING_IMAGE_NAME,) if self.tuning else ()
        return [
            f"{name}:{self.image_tag}" for name in (*IMAGE_NAMES, *BUILD_ONLY_IMAGE_NAMES, *extra)
        ]

    @property
    def kind_cluster(self) -> str:
        return self.context.removeprefix(KIND_CONTEXT_PREFIX)


def step(number: int, description: str) -> None:
    print(f"\n=== [{number}/{STEP_COUNT}] {description}", flush=True)


def run_command(
    command: list[str], *, capture: bool = False, check_exit: bool = True
) -> subprocess.CompletedProcess[str]:
    print(f"$ {shlex.join(command)}", flush=True)
    return subprocess.run(  # noqa: S603 - fixed argument lists, never a shell
        command, check=check_exit, text=True, capture_output=capture
    )


def kubectl(
    deployment: Deployment, *args: str, namespaced: bool = True, capture: bool = False
) -> subprocess.CompletedProcess[str]:
    scope = ["--namespace", deployment.namespace] if namespaced else []
    return run_command(["kubectl", "--context", deployment.context, *scope, *args], capture=capture)


def kubectl_stdout(deployment: Deployment, *args: str) -> str:
    """A cluster-wide kubectl call's standard output (--tuning reads the target namespace)."""
    return kubectl(deployment, *args, namespaced=False, capture=True).stdout


def api_pod_exec(deployment: Deployment, *command: str) -> str:
    result = kubectl(
        deployment,
        "exec",
        f"deploy/{deployment.release}-api",
        "-c",
        "api",
        "--",
        *command,
        capture=True,
    )
    return result.stdout


# --- 1. preconditions -------------------------------------------------------------------------


def require_context(deployment: Deployment) -> None:
    current = run_command(["kubectl", "config", "current-context"], capture=True).stdout.strip()
    if current != deployment.context:
        raise E2EError(
            f"the current kube context is {current!r} but {CONTEXT_ENV} is {deployment.context!r}: "
            f"run `kubectl config use-context {deployment.context}` or set {CONTEXT_ENV}"
        )
    if not deployment.context.startswith(KIND_CONTEXT_PREFIX):
        raise E2EError(f"{CONTEXT_ENV}={deployment.context!r} is not a kind context (kind-<name>)")
    print(f"ok   kube context {deployment.context} (kind cluster {deployment.kind_cluster})")


def require_cluster_issuer(deployment: Deployment) -> None:
    if deployment.mode is not Security.MUTUAL_TLS:
        return
    ready = run_command(
        [
            "kubectl",
            "--context",
            deployment.context,
            "get",
            "clusterissuer",
            CLUSTER_ISSUER,
            "-o",
            'jsonpath={.status.conditions[?(@.type=="Ready")].status}',
        ],
        capture=True,
        check_exit=False,
    )
    if ready.stdout.strip() != "True":
        raise E2EError(
            f"the ClusterIssuer {CLUSTER_ISSUER!r} is missing or not Ready. This script never "
            f"applies it: review {CLUSTER_ISSUER_FILE} and `kubectl apply -f` it yourself"
        )
    print(f"ok   ClusterIssuer {CLUSTER_ISSUER} is Ready")


# --- 2. images --------------------------------------------------------------------------------


def build_and_load_images(deployment: Deployment, stack: ExitStack) -> None:
    if not deployment.keep:
        # Registered before the build, so a half-finished build or load is cleaned up too.
        stack.callback(remove_images, deployment)
    run_command(
        [
            "just",
            "--justfile",
            str(REPOSITORY / "Justfile"),
            f"image_tag={deployment.image_tag}",
            *IMAGE_RECIPES,
            *((TUNING_IMAGE_RECIPE,) if deployment.tuning else ()),
        ]
    )
    run_command(
        [
            "kind",
            "load",
            "docker-image",
            *deployment.images,
            "--name",
            deployment.kind_cluster,
        ]
    )


# --- 3. install -------------------------------------------------------------------------------


def create_namespace(deployment: Deployment, stack: ExitStack) -> None:
    # `create` fails if the namespace exists, so a namespace we delete is always one we made:
    # cleanup is registered first, but only acts once the create succeeded.
    created: list[bool] = []
    if not deployment.keep:
        stack.callback(lambda: delete_namespace(deployment) if created else None)
    try:
        kubectl(deployment, "create", "namespace", deployment.namespace, namespaced=False)
    except subprocess.CalledProcessError as error:
        raise E2EError(
            f"could not create namespace {deployment.namespace} (a leftover from an earlier "
            f"--keep run? delete it yourself: kubectl delete namespace {deployment.namespace})"
        ) from error
    created.append(True)


def create_target_namespace(deployment: Deployment, stack: ExitStack) -> None:
    """--tuning: the managed vLLM's namespace; it must exist before the chart puts a Role in it."""
    if not deployment.keep:
        stack.callback(
            lambda: kubectl_quiet(
                deployment,
                "delete",
                "namespace",
                deployment.target_namespace,
                "--wait=true",
                f"--timeout={CLEANUP_TIMEOUT}",
            )
        )
    try:
        kubectl(
            deployment,
            "create",
            "namespace",
            deployment.target_namespace,
            namespaced=False,
        )
    except subprocess.CalledProcessError as error:
        raise E2EError(
            f"could not create namespace {deployment.target_namespace}: delete a leftover yourself"
        ) from error


def create_host_secrets(deployment: Deployment) -> None:
    """--tuning-host: the launcher token and vLLM's key as Secrets in the runs namespace, passed to
    kubectl on stdin so the values are never in a command line."""
    for secret, variable in (
        (LAUNCHER_TOKEN_SECRET, "SERVTUNER_E2E_LAUNCHER_TOKEN"),
        (VLLM_KEY_SECRET, "SERVTUNER_E2E_VLLM_KEY"),
    ):
        subprocess.run(
            [
                "kubectl",
                "--context",
                deployment.context,
                "--namespace",
                deployment.namespace,
                "create",
                "secret",
                "generic",
                secret,
                "--from-file=token=/dev/stdin",
            ],
            input=os.environ[variable].strip(),
            text=True,
            check=True,
            capture_output=True,
        )


def install_chart(
    deployment: Deployment, stack: ExitStack, extra_values: tuple[Path, ...] = ()
) -> None:
    settings = {
        "ingress.host": deployment.host,
        "frontend.basePath": UI_BASE_PATH,
        # The images just built and loaded, not the published ones the chart defaults to.
        "api.image.repository": "servtuner-api",
        "api.image.tag": deployment.image_tag,
        "frontend.image.repository": "servtuner-frontend",
        "frontend.image.tag": deployment.image_tag,
    }
    if deployment.runs_jobs:
        settings["bench.image.repository"] = "servtuner-bench"
        settings["bench.image.tag"] = deployment.image_tag
    if deployment.tuning:
        settings["rbac.tuning.namespaces[0]"] = deployment.target_namespace
    if deployment.mode is Security.MUTUAL_TLS:
        settings["tls.certManager.issuerRef.name"] = CLUSTER_ISSUER
    else:
        # Without TLS the api signs in to Postgres with the generated password.
        settings["postgres.auth"] = "password"
    # --set-string: a host or tag with a comma or a number-like value cannot inject or be coerced.
    set_arguments = [f"--set-string={key}={value}" for key, value in settings.items()]
    # tls.enabled must reach the chart as a boolean, so it is the one plain --set.
    set_arguments.append(f"--set=tls.enabled={deployment.mode.tls_flag}")
    if not deployment.keep:
        # Registered first, so it runs even if the install itself half-succeeded.
        stack.callback(uninstall_chart, deployment)
    run_command(
        [
            "helm",
            "--kube-context",
            deployment.context,
            "upgrade",
            "--install",
            deployment.release,
            str(CHART),
            "--namespace",
            deployment.namespace,
            "--values",
            str(E2E_VALUES),
            *(["--values", str(LOAD_VALUES)] if deployment.load else []),
            *(["--values", str(JOBS_VALUES)] if deployment.runs_jobs else []),
            *(
                ["--values", str(TUNING_VALUES)]
                if deployment.tuning or deployment.tuning_host
                else []
            ),
            *(argument for path in extra_values for argument in ("--values", str(path))),
            *set_arguments,
        ]
    )


def wait_until_ready(deployment: Deployment) -> None:
    kubectl(
        deployment,
        "wait",
        "--for=condition=Ready",
        f"cluster.postgresql.cnpg.io/{deployment.release}-pg",
        f"--timeout={WAIT_TIMEOUT}",
    )
    for component in ("api", "frontend"):
        kubectl(
            deployment,
            "rollout",
            "status",
            f"deployment/{deployment.release}-{component}",
            f"--timeout={WAIT_TIMEOUT}",
        )


# --- 4. reaching the ingress ------------------------------------------------------------------


def forward_port(
    deployment: Deployment,
    stack: ExitStack,
    scratch: Path,
    namespace: str,
    target: str,
    remote_port: int,
) -> int:
    """``kubectl port-forward`` on local port 0; returns the port kubectl chose."""
    # One log per forward: the port is read from it, and a target may be forwarded more than once.
    log_path = scratch / f"port-forward-{target.replace('/', '-')}-{time.monotonic_ns()}.log"
    command = [
        "kubectl",
        "--context",
        deployment.context,
        "--namespace",
        namespace,
        "port-forward",
        target,
        f":{remote_port}",
        "--address",
        LOOPBACK,
    ]
    print(f"$ {shlex.join(command)}", flush=True)
    with log_path.open("wb") as log:
        process = subprocess.Popen(command, stdout=log, stderr=subprocess.STDOUT)  # noqa: S603
    stack.callback(stop_process, process)
    deadline = time.monotonic() + PORT_FORWARD_TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        match = FORWARDING_LINE.search(log_path.read_text())
        if match:
            return int(match.group(1))
        if process.poll() is not None:
            break
        time.sleep(0.2)
    raise E2EError(f"port-forward to {target} did not start:\n{log_path.read_text()}")


def ingress_client(deployment: Deployment, port: int, path: str = "") -> httpx2.Client:
    # The Host header selects the Ingress; no /etc/hosts edit and no DNS needed. Behind the
    # forward-header stub every request needs a login; the exercise overrides it per request.
    headers = {"Host": deployment.host}
    if deployment.forward_header:
        headers |= as_subject(ADMIN_SUBJECT)
    return httpx2.Client(base_url=f"http://{LOOPBACK}:{port}{path}", headers=headers)


def install_forward_auth_stub(deployment: Deployment, scratch: Path) -> Path:
    """The stub auth service (e2e/forward_header.py) and the chart values that point at it."""
    manifests = scratch / "forward-auth-stub.json"
    image = f"servtuner-api:{deployment.image_tag}"
    manifests.write_text(stub_manifests(deployment.namespace, image))
    kubectl(deployment, "apply", "-f", str(manifests))
    kubectl(
        deployment,
        "rollout",
        "status",
        f"deployment/{STUB_NAME}",
        f"--timeout={WAIT_TIMEOUT}",
    )
    return write_chart_values(
        deployment.namespace,
        scratch / "forward-header-values.json",
        is_gateway=deployment.gateway,
    )


def create_gateway(deployment: Deployment, scratch: Path) -> Path:
    """A Gateway of Envoy Gateway's class in the run's namespace, and the chart values that route
    through it instead of the Ingress. It goes with the namespace at cleanup."""
    gateway = {
        "apiVersion": "gateway.networking.k8s.io/v1",
        "kind": "Gateway",
        "metadata": {"name": GATEWAY_NAME, "namespace": deployment.namespace},
        "spec": {
            "gatewayClassName": GATEWAY_CLASS,
            "listeners": [{"name": "http", "protocol": "HTTP", "port": INGRESS_HTTP_PORT}],
        },
    }
    manifest = scratch / "gateway.json"
    manifest.write_text(json.dumps(gateway))
    kubectl(deployment, "apply", "-f", str(manifest))
    kubectl(
        deployment,
        "wait",
        f"gateway/{GATEWAY_NAME}",
        "--for=condition=Programmed",
        f"--timeout={WAIT_TIMEOUT}",
    )
    values = scratch / "gateway-values.json"
    values.write_text(
        json.dumps(
            {
                "ingress": {"enabled": False},
                "gateway": {
                    "enabled": True,
                    "hostname": deployment.host,
                    "parentRefs": [{"name": GATEWAY_NAME}],
                },
            }
        )
    )
    return values


def gateway_service(deployment: Deployment) -> str:
    """The Envoy proxy Service of the run's Gateway, found by its owning-gateway labels."""
    selector = (
        f"gateway.envoyproxy.io/owning-gateway-namespace={deployment.namespace},"
        f"gateway.envoyproxy.io/owning-gateway-name={GATEWAY_NAME}"
    )
    name = kubectl(
        deployment,
        "get",
        "svc",
        "--namespace",
        GATEWAY_NAMESPACE,
        f"--selector={selector}",
        "-o",
        "jsonpath={.items[0].metadata.name}",
        namespaced=False,
        capture=True,
    ).stdout.strip()
    if not name:
        raise E2EError(f"no Envoy proxy Service for gateway {GATEWAY_NAME}")
    return name


def check_backend_hostname_is_verified(deployment: Deployment, api: httpx2.Client) -> None:
    """Mutual TLS through the gateway: a wrong BackendTLSPolicy hostname must break the connection,
    so the api's certificate is checked by name and not only by CA. The policy is restored after.
    """
    policy = f"backendtlspolicy/{deployment.release}-api"
    right = f"{deployment.release}-api.{deployment.namespace}.svc"

    def set_hostname(hostname: str) -> None:
        patch = json.dumps({"spec": {"validation": {"hostname": hostname}}})
        kubectl(deployment, "patch", policy, "--type=merge", "-p", patch)

    set_hostname("not-the-api.invalid")
    try:
        wait_for(
            "the gateway to refuse an api certificate without the expected name",
            lambda: api.get("/healthz").status_code >= 500,
            ROUTE_TIMEOUT_SECONDS,
        )
        print(
            "ok   mutual-tls: a BackendTLSPolicy hostname the api certificate lacks breaks the "
            "connection"
        )
    finally:
        set_hostname(right)
    wait_for(
        "the api through the gateway again",
        lambda: api.get("/healthz").is_success,
        ROUTE_TIMEOUT_SECONDS,
    )


def setup_token(deployment: Deployment) -> str:
    """The chart's setup token Secret, as NOTES.txt says to read it."""
    encoded = kubectl(
        deployment,
        "get",
        "secret",
        f"{deployment.release}-setup-token",
        "-o",
        "jsonpath={.data.token}",
        capture=True,
    ).stdout
    return base64.b64decode(encoded).decode()


# --- 5. the run, through the ingress ----------------------------------------------------------


def api_path_from_config(deployment: Deployment, ui: httpx2.Client) -> str:
    """The path the UI would call: config.json's ``apiBaseUrl``, resolved against the base path."""
    config = ui.get(f"{UI_BASE_PATH}config.json")
    check(
        config.status_code == 200,
        f"config.json is served under {UI_BASE_PATH} ({config.status_code})",
    )
    api_base_url: str = config.json()["apiBaseUrl"]
    resolved = urljoin(f"http://{deployment.host}{UI_BASE_PATH}", api_base_url)
    path = urlparse(resolved).path.rstrip("/")
    print(f"ok   config.json apiBaseUrl {api_base_url!r} resolves to {path}", flush=True)
    return path


def check_bundle_in_pod(deployment: Deployment, run_id: str) -> None:
    data_dir = Path(api_pod_exec(deployment, "printenv", "SERVTUNER_DATA_DIR").strip())
    bundle_dir = run_bundle_dir(data_dir, run_id)
    names = api_pod_exec(deployment, "ls", "-1", str(bundle_dir)).split()
    check(
        "manifest.json" in names,
        f"the api pod's data dir holds the bundle ({', '.join(names)})",
    )
    manifest = api_pod_exec(deployment, "cat", str(bundle_dir / "manifest.json"))
    check_manifest_succeeded(json.loads(manifest))


def provide_example_secrets(deployment: Deployment, scratch: Path) -> None:
    """A Job mounts every Secret its RunSpec names, and the runner refuses a run whose Secret is
    missing. The mutual-tls example names a target client certificate; supply one, as an operator
    would, by copying bench's own certificate. It lives in this run's namespace and goes with it.
    """
    if not (deployment.runs_jobs and deployment.mode is Security.MUTUAL_TLS):
        return
    source = f"{deployment.release}-bench-tls"
    cert = secret_file(deployment, source, "tls.crt", scratch / "client.crt")
    key = secret_file(deployment, source, "tls.key", scratch / "client.key")
    kubectl(
        deployment,
        "create",
        "secret",
        "tls",
        EXAMPLE_TARGET_CLIENT_SECRET,
        f"--cert={cert}",
        f"--key={key}",
    )


def check_ran_as_job(deployment: Deployment, run_id: str) -> None:
    """With the Kubernetes runner, the run was a Job of its own that completed."""
    if not deployment.runs_jobs:
        return
    listing = kubectl(
        deployment,
        "get",
        "jobs",
        "--selector",
        f"{RUN_ID_LABEL}={run_id.lower()}",
        "--output",
        "jsonpath={range .items[*]}{.metadata.name} {.status.succeeded}{'\\n'}{end}",
        capture=True,
    ).stdout.split()
    check(
        listing == [f"bench-{run_id.lower()}", "1"],
        f"bench ran as Job bench-{run_id.lower()}, which completed ({listing})",
    )


# The sharded run's shard count: the leader and one follower.
SHARDS = 2


def check_ran_as_indexed_job(deployment: Deployment, run_id: str) -> None:
    """A sharded run was one Indexed Job whose every shard pod completed."""
    if not deployment.runs_jobs:
        return
    listing = kubectl(
        deployment,
        "get",
        "jobs",
        "--selector",
        f"{RUN_ID_LABEL}={run_id.lower()}",
        "--output",
        "jsonpath={range .items[*]}{.spec.completionMode} {.status.succeeded}{'\\n'}{end}",
        capture=True,
    ).stdout.split()
    check(
        listing == ["Indexed", str(SHARDS)],
        f"the sharded run was an Indexed Job with {SHARDS} completed shards ({listing})",
    )
    data_dir = Path(api_pod_exec(deployment, "printenv", "SERVTUNER_DATA_DIR").strip())
    shard_dir = run_bundle_dir(data_dir, run_id) / "shards" / "1"
    files = api_pod_exec(deployment, "ls", "-1", str(shard_dir)).split()
    check("done.json" in files, f"the follower shard handed its rows over ({files})")


def require_load_targets(deployment: Deployment) -> None:
    """--load drives traffic at the mock and servtuner's Bifrost; this script never installs
    them."""
    for namespace, service in LOAD_TARGET_SERVICES:
        found = kubectl_quiet(deployment, "get", "service", service, "--namespace", namespace)
        if found.returncode != 0:
            raise E2EError(
                f"--load needs service {namespace}/{service}; install it first "
                "(deploy/mocks/README.md)"
            )
    print("ok   the mock and servtuner's Bifrost are installed")


# Run in the api pod (its image has Python): summarise a load run's bundle without copying it out.
LOAD_BUNDLE_SUMMARY = """
import json, os, sys
bundle = sys.argv[1]
def read(name):
    with open(os.path.join(bundle, name)) as handle:
        return json.load(handle)
manifest = read("manifest.json")
queries = read("prometheus/queries.json")
snapshot = read("target/snapshot-pre.json")
analysis = read("analysis.json")
environment = read("environment.json")
collected = {}
for query in queries.get("queries", []):
    if query.get("result") == "collected":
        collected[query["catalog"]] = collected.get(query["catalog"], 0) + 1
print(json.dumps({
    "status": manifest["status"],
    "files": sorted(entry["path"] for entry in manifest["files"]),
    "collected": collected,
    "pods": [len(workload.get("pods", [])) for workload in snapshot.get("workloads", [])],
    "grade": analysis["quality"]["grade"],
    "stages": [
        {
            "index": stage["stage_index"],
            "offered": stage["offered_rps"],
            "achieved": stage["achieved_rps"],
            "requests": stage["requests"],
            "errors": stage["errors"],
            "send_lag_p99": stage.get("send_lag_p99_seconds"),
            "send_lag_max": stage.get("send_lag_max_seconds"),
        }
        for stage in analysis["stages"]
    ],
    "valid": [stage["is_valid"] for stage in analysis["quality"]["stages"]],
    "client_bound": [stage["is_client_bound"] for stage in analysis["quality"]["stages"]],
    "issues": [
        f"{issue['code']}: {issue['message']}" for issue in analysis["quality"].get("issues", [])
    ],
    "stage_issues": sorted({
        f"{issue['code']}: {issue['message']}"
        for stage in analysis["quality"]["stages"] for issue in stage.get("issues", [])
    }),
    "capacity": analysis["capacity"],
    "pod": environment.get("pod"),
    "knobs": sorted(environment.get("knobs", {})),
}))
"""

# Every load bundle holds these, beyond the manifest.
LOAD_BUNDLE_FILES = (
    "requests.parquet",
    "client/samples.parquet",
    "phases.json",
    "environment.json",
    "target/snapshot-pre.json",
    "target/snapshot-post.json",
    "prometheus/targets.json",
    "prometheus/queries.json",
    "analysis.json",
    "summary.json",
)


def check_load_bundle_in_pod(deployment: Deployment, run_id: str, catalog: str) -> None:
    data_dir = Path(api_pod_exec(deployment, "printenv", "SERVTUNER_DATA_DIR").strip())
    bundle_dir = run_bundle_dir(data_dir, run_id)
    summary = json.loads(
        api_pod_exec(deployment, "python", "-c", LOAD_BUNDLE_SUMMARY, str(bundle_dir))
    )
    print(f"     bundle: {json.dumps(summary)}")
    check(summary["status"] == "succeeded", "the bundle manifest says succeeded")
    missing = [name for name in LOAD_BUNDLE_FILES if name not in summary["files"]]
    check(
        not missing,
        f"the bundle holds records, snapshot, Prometheus, analysis ({missing=})",
    )
    series = [
        name for name in summary["files"] if name.startswith("prometheus/") and name.count("/") == 2
    ]
    check(bool(series), f"Prometheus series were stored ({len(series)} files)")
    for expected in ("kubernetes", catalog):
        check(
            summary["collected"].get(expected, 0) > 0,
            f"catalog {expected} returned series",
        )
    pods = summary["pods"]
    check(bool(pods) and all(pods), f"the snapshot found the target's pods {pods}")
    check(summary["pod"] is not None, "environment.json names the bench pod")
    if deployment.runs_jobs:
        check(
            summary["pod"].startswith(f"bench-{run_id.lower()}-"),
            f"bench ran in the Job's pod ({summary['pod']})",
        )
    check_ran_as_job(deployment, run_id)
    check(
        summary["grade"] != "unusable",
        f"the quality gate graded the run {summary['grade']}",
    )
    if deployment.runspecs:
        # An experiment is meant to find the limit, so its last stages may fail: report them.
        print(f"     stages: {summary['stages']} valid: {summary['valid']}")
        return
    check(all(summary["valid"]), f"every stage passed the quality gate {summary['valid']}")
    check(
        all(stage["errors"] == 0 and stage["requests"] > 0 for stage in summary["stages"]),
        f"every stage served requests without errors {summary['stages']}",
    )


def report_calibration(deployment: Deployment, run_id: str) -> None:
    """Print a calibration run's per-stage numbers. A stage failing the quality gate is a result
    here (it marks the ceiling), not a failure of the script."""
    data_dir = Path(api_pod_exec(deployment, "printenv", "SERVTUNER_DATA_DIR").strip())
    bundle_dir = run_bundle_dir(data_dir, run_id)
    summary = json.loads(
        api_pod_exec(deployment, "python", "-c", LOAD_BUNDLE_SUMMARY, str(bundle_dir))
    )
    check(
        summary["status"] == "succeeded",
        "the calibration bundle's manifest says succeeded",
    )
    print(f"     calibration {run_id}: grade {summary['grade']}")
    for stage, valid, bound in zip(
        summary["stages"], summary["valid"], summary["client_bound"], strict=True
    ):
        print(f"     calibration stage: {json.dumps(stage)} valid={valid} client_bound={bound}")
    for issue in summary["stage_issues"]:
        print(f"     calibration issue: {issue}")


def check_ui_lists_run(
    deployment: Deployment, ui: httpx2.Client, api_path: str, run_id: str
) -> None:
    index = ui.get(UI_BASE_PATH)
    check(
        index.status_code == 200,
        f"index.html is served under {UI_BASE_PATH} ({index.status_code})",
    )
    check(
        f'<base href="{UI_BASE_PATH}"' in index.text,
        f"index.html carries <base href={UI_BASE_PATH}>",
    )
    listing = ui.get(f"{api_path}/runs")
    check(
        run_id in listing.text,
        f"GET {api_path}/runs through the ingress contains {run_id}",
    )


# --- 6. mutual-tls: the api refuses an anonymous client ----------------------------------------


def secret_file(deployment: Deployment, secret: str, key: str, destination: Path) -> Path:
    data = json.loads(
        kubectl(deployment, "get", "secret", secret, "-o", "json", capture=True).stdout
    )
    destination.write_bytes(base64.b64decode(data["data"][key]))
    destination.chmod(0o600)
    return destination


def check_api_refuses_anonymous_client(
    deployment: Deployment, stack: ExitStack, scratch: Path
) -> None:
    api_service = f"{deployment.release}-api"
    port = forward_port(
        deployment, stack, scratch, deployment.namespace, f"svc/{api_service}", 8080
    )
    # The certificate has the SAN "localhost" for the SubprocessRunner (chart certificates.yaml).
    base_url = f"https://localhost:{port}"
    ca_file = secret_file(deployment, f"{api_service}-tls", "ca.crt", scratch / "ca.crt")
    refuse_without_client_certificate(base_url, ca_file)
    # Positive control: the ingress's own client certificate is accepted, so the refusal above is
    # about the missing identity and not about trust. Through a new port-forward: kubectl may end
    # a port-forward once the pod resets one of its connections, as the refused handshake did.
    port = forward_port(
        deployment, stack, scratch, deployment.namespace, f"svc/{api_service}", 8080
    )
    ingress_secret = f"{deployment.release}-ingress-tls"
    client_certificate = (
        secret_file(deployment, ingress_secret, "tls.crt", scratch / "client.crt"),
        secret_file(deployment, ingress_secret, "tls.key", scratch / "client.key"),
    )
    with httpx2.Client(
        base_url=f"https://localhost:{port}", verify=client_context(ca_file, client_certificate)
    ) as client:
        check(
            client.get("/healthz").is_success,
            "mutual-tls: the ingress's client certificate is accepted",
        )


# --- 7. cleanup and diagnostics -----------------------------------------------------------------


def uninstall_chart(deployment: Deployment) -> None:
    print(f"cleanup: helm uninstall {deployment.release}", flush=True)
    result = run_command(
        [
            "helm", "--kube-context", deployment.context, "uninstall", deployment.release,
            "--namespace", deployment.namespace, "--wait", "--timeout", CLEANUP_TIMEOUT,
        ],
        check_exit=False,
    )  # fmt: skip
    if result.returncode != 0:
        deployment.problems.append(
            f"helm uninstall {deployment.release} exited {result.returncode}"
        )
    remove_cluster_scoped_leftovers(deployment)


def remove_cluster_scoped_leftovers(deployment: Deployment) -> None:
    """The chart's ClusterRole and ClusterRoleBinding live outside the namespace; only helm removes
    them. Delete exactly these two names if helm left them behind."""
    for kind in CLUSTER_SCOPED_KINDS:
        name = f"{deployment.release}{CLUSTER_SCOPED_SUFFIX}"
        found = kubectl_quiet(deployment, "get", kind, name, "--ignore-not-found", "-o", "name")
        if not found.stdout.strip():
            continue
        deployment.problems.append(f"helm left {kind}/{name}; deleting exactly that")
        kubectl_quiet(deployment, "delete", kind, name, "--ignore-not-found")
        if kubectl_quiet(
            deployment, "get", kind, name, "--ignore-not-found", "-o", "name"
        ).stdout.strip():
            deployment.problems.append(f"{kind}/{name} STILL EXISTS; delete it by hand")


def kubectl_quiet(deployment: Deployment, *args: str) -> subprocess.CompletedProcess[str]:
    return run_command(
        ["kubectl", "--context", deployment.context, *args],
        capture=True,
        check_exit=False,
    )


def remove_images(deployment: Deployment) -> None:
    """Remove exactly this run's image tags from every kind node (containerd) and from docker."""
    print(f"cleanup: remove images tagged {deployment.image_tag}", flush=True)
    nodes = run_command(
        ["kind", "get", "nodes", "--name", deployment.kind_cluster],
        capture=True,
        check_exit=False,
    ).stdout.split()
    for image in deployment.images:
        for node in nodes:
            run_command(
                ["docker", "exec", node, "crictl", "rmi", f"{CONTAINERD_IMAGE_PREFIX}{image}"],
                capture=True, check_exit=False,
            )  # fmt: skip
    for node in nodes:
        listing = run_command(
            ["docker", "exec", node, "crictl", "images"], capture=True, check_exit=False
        )
        if f" {deployment.image_tag} " in listing.stdout:
            deployment.problems.append(
                f"image tag {deployment.image_tag} still on kind node {node}"
            )
    run_command(["docker", "rmi", *deployment.built_images], capture=True, check_exit=False)


def delete_namespace(deployment: Deployment) -> None:
    print(f"cleanup: delete namespace {deployment.namespace}", flush=True)
    run_command(
        [
            "kubectl",
            "--context",
            deployment.context,
            "delete",
            "namespace",
            deployment.namespace,
            "--ignore-not-found",
            "--wait",
            f"--timeout={CLEANUP_TIMEOUT}",
        ],
        check_exit=False,
    )


def print_diagnostics(deployment: Deployment) -> None:
    """Best effort: what a person debugging a failed run would look at first."""
    print("\n--- diagnostics ---", flush=True)
    queries = [
        (
            "get",
            "pods,pvc,ingress,certificate,cluster.postgresql.cnpg.io",
            "-o",
            "wide",
        ),
        ("get", "events", "--sort-by=.lastTimestamp"),
        ("logs", f"deploy/{deployment.release}-api", "--all-containers", "--tail=60"),
        ("logs", f"deploy/{deployment.release}-frontend", "--tail=20"),
    ]
    for query in queries:
        try:
            result = kubectl(deployment, *query, capture=True)
        except (subprocess.CalledProcessError, OSError) as error:
            print(f"(diagnostic failed: {error})", flush=True)
            continue
        print(result.stdout[-6000:], flush=True)


# --- the whole run ------------------------------------------------------------------------------


def run_e2e(deployment: Deployment, stack: ExitStack) -> None:
    scratch = Path(stack.enter_context(scratch_directory()))
    mode = deployment.mode
    print(
        f"e2e-cluster: mode {mode.value}, namespace {deployment.namespace}, host {deployment.host}"
    )

    step(1, "check the kube context and prerequisites")
    require_context(deployment)
    require_cluster_issuer(deployment)
    if deployment.load:
        require_load_targets(deployment)

    step(2, "build the images and load them into kind")
    build_and_load_images(deployment, stack)

    step(3, f"create namespace {deployment.namespace} and install the chart")
    create_namespace(deployment, stack)
    if deployment.tuning:
        create_target_namespace(deployment, stack)
    try:
        extra_values: tuple[Path, ...] = ()
        if deployment.gateway:
            extra_values += (create_gateway(deployment, scratch),)
        if deployment.forward_header:
            extra_values += (install_forward_auth_stub(deployment, scratch),)
        install_chart(deployment, stack, extra_values)

        step(4, "wait for Postgres, the api and the frontend")
        wait_until_ready(deployment)
        provide_example_secrets(deployment, scratch)

        if deployment.gateway:
            step(5, "reach the gateway through a port-forward")
            port = forward_port(
                deployment,
                stack,
                scratch,
                GATEWAY_NAMESPACE,
                f"svc/{gateway_service(deployment)}",
                INGRESS_HTTP_PORT,
            )
        else:
            step(5, "reach the ingress through a port-forward")
            port = forward_port(
                deployment,
                stack,
                scratch,
                INGRESS_NAMESPACE,
                f"svc/{INGRESS_SERVICE}",
                INGRESS_HTTP_PORT,
            )
        with ingress_client(deployment, port) as ui:
            wait_for(
                "the UI through the ingress",
                lambda: ui.get(UI_BASE_PATH).is_success,
                ROUTE_TIMEOUT_SECONDS,
            )
            api_path = api_path_from_config(deployment, ui)
            with ingress_client(deployment, port, api_path) as api:
                wait_for(
                    "the api through the ingress",
                    lambda: api.get("/healthz").is_success,
                    ROUTE_TIMEOUT_SECONDS,
                )

                if deployment.gateway and mode is Security.MUTUAL_TLS:
                    check_backend_hostname_is_verified(deployment, api)
                if deployment.forward_header:
                    step(
                        6,
                        "forward-header auth: setup, refusals, a forged header, a viewer, a run",
                    )
                    run_id = exercise_forward_header(
                        api,
                        setup_token(deployment),
                        mode,
                        partial(check_bundle_in_pod, deployment),
                    )
                    probe_network_policy(
                        partial(kubectl_quiet, deployment),
                        deployment.namespace,
                        f"servtuner-api:{deployment.image_tag}",
                        f"http://{deployment.release}-api.{deployment.namespace}.svc:8080",
                    )
                elif deployment.ui:
                    step(
                        6,
                        "a managed vLLM from the UI: deploy, log, redeploy, benchmark, tune",
                    )
                    if deployment.tuning_host:
                        create_host_secrets(deployment)
                        ui_target = host_ui_target(
                            launcher_url=os.environ["SERVTUNER_E2E_LAUNCHER_URL"],
                            endpoint=os.environ["SERVTUNER_E2E_VLLM_ENDPOINT"],
                            launcher_token_secret=LAUNCHER_TOKEN_SECRET,
                            vllm_key_secret=VLLM_KEY_SECRET,
                        )
                    else:
                        ui_target = kubernetes_ui_target(
                            image=f"{TUNING_IMAGE_NAME}:{deployment.image_tag}",
                            target_namespace=deployment.target_namespace,
                        )
                    run_id = exercise_managed_ui(
                        api,
                        # *.localtest.me resolves to loopback, where the ingress is forwarded.
                        ui_url=f"http://{deployment.host}:{port}{UI_BASE_PATH}",
                        target=ui_target,
                        target_namespace=(
                            None if deployment.tuning_host else deployment.target_namespace
                        ),
                        kubectl_output=partial(kubectl_stdout, deployment),
                    )
                elif deployment.tuning_host:
                    step(6, "tune the GPU host's vLLM through its launcher")
                    create_host_secrets(deployment)
                    run_id = exercise_tuning_host(
                        api,
                        launcher_url=os.environ["SERVTUNER_E2E_LAUNCHER_URL"],
                        endpoint=os.environ["SERVTUNER_E2E_VLLM_ENDPOINT"],
                        launcher_token_secret=LAUNCHER_TOKEN_SECRET,
                        vllm_key_secret=VLLM_KEY_SECRET,
                    )
                elif deployment.tuning:
                    step(
                        6,
                        "tune the mock as a managed vLLM: boot probes, runs, a recommendation",
                    )
                    run_id = exercise_tuning(
                        api,
                        image=f"{TUNING_IMAGE_NAME}:{deployment.image_tag}",
                        target_namespace=deployment.target_namespace,
                        kubectl_output=partial(kubectl_stdout, deployment),
                    )
                elif deployment.calibrate:
                    step(6, "run the calibration ladders against the mock")
                    run_ids = [
                        exercise_run(
                            api,
                            mode,
                            partial(report_calibration, deployment),
                            document=runspec.read_bytes(),
                            timeout_seconds=LOAD_RUN_TIMEOUT_SECONDS,
                        )
                        for runspec in CALIBRATION_RUNSPECS
                    ]
                    run_id = run_ids[-1]
                elif deployment.load:
                    step(6, "run real load against the mock and through Bifrost")
                    run_ids: list[str] = []
                    for runspec in deployment.runspecs or LOAD_RUNSPECS:
                        document = runspec.read_bytes()
                        target_catalog = json.loads(document)["target"]["type"]
                        if target_catalog == "openai-compatible":
                            target_catalog = "kubernetes"
                        print(f"     {runspec.name}")
                        run_ids.append(
                            exercise_run(
                                api,
                                mode,
                                partial(
                                    check_load_bundle_in_pod,
                                    deployment,
                                    catalog=target_catalog,
                                ),
                                document=document,
                                timeout_seconds=LOAD_RUN_TIMEOUT_SECONDS,
                            )
                        )
                    # The UI check below looks for the last run.
                    run_id = run_ids[-1]
                else:
                    step(6, "create a run through the api and wait for it")

                    def check_run(run_id: str) -> None:
                        check_bundle_in_pod(deployment, run_id)
                        check_ran_as_job(deployment, run_id)

                    run_id = exercise_run(api, mode, check_run)

                    step(6, "create a sharded run: an Indexed Job with one pod per shard")
                    sharded = json.loads(example_bytes("runspec", "valid", EXAMPLE_FOR_MODE[mode]))
                    sharded["engine"] = {"type": "loadgen", "shards": SHARDS}

                    def check_sharded(run_id: str) -> None:
                        check_bundle_in_pod(deployment, run_id)
                        check_ran_as_indexed_job(deployment, run_id)

                    exercise_run(api, mode, check_sharded, document=json.dumps(sharded).encode())

            step(7, "check the UI lists the run")
            check_ui_lists_run(deployment, ui, api_path, run_id)

        step(8, "mutual-tls: a direct connection without a client certificate is refused")
        if mode is Security.MUTUAL_TLS:
            check_api_refuses_anonymous_client(deployment, stack, scratch)
        else:
            print("skipped: tls-off has no client certificates")
    except BaseException:
        print_diagnostics(deployment)
        raise


def install_signal_handlers() -> None:
    def raise_exit(signal_number: int, _frame: FrameType | None) -> None:
        raise SystemExit(128 + signal_number)

    for signal_number in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(signal_number, raise_exit)


def ignore_signals_during_cleanup() -> None:
    for signal_number in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
        signal.signal(signal_number, signal.SIG_IGN)


def parse_arguments(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0] if __doc__ else None)
    parser.add_argument("mode", choices=[mode.value for mode in Security])
    parser.add_argument(
        "--image-tag",
        required=True,
        help="prefix of the per-run tag of every image (the Justfile's image_tag)",
    )
    parser.add_argument(
        "--load",
        action="store_true",
        help="real load runs against the installed mock and servtuner's Bifrost",
    )
    parser.add_argument(
        "--calibrate",
        action="store_true",
        help="calibration: engine ladders against the installed mock (implies --load)",
    )
    parser.add_argument(
        "--runner",
        choices=RUNNERS,
        default="subprocess",
        help="who runs bench: the api pod (subprocess) or a Job per run (kubernetes)",
    )
    parser.add_argument(
        "--runspec",
        action="append",
        type=Path,
        default=[],
        help="with --load: run this RunSpec instead of the defaults (repeatable)",
    )
    parser.add_argument(
        "--tuning",
        action="store_true",
        help="tune the mock as a managed vLLM (needs --runner kubernetes, tls-off)",
    )
    parser.add_argument(
        "--tuning-host",
        action="store_true",
        help="tune a GPU host's vLLM through servtuner launcher (needs --runner kubernetes, "
        "tls-off, and the SERVTUNER_E2E_* host variables)",
    )
    parser.add_argument(
        "--ui",
        action="store_true",
        help="with --tuning or --tuning-host: drive the managed vLLM through the UI (Playwright, "
        "needs node and Chrome); the ingress host is a *.localtest.me name: no hosts entry needed",
    )
    parser.add_argument(
        "--forward-header",
        action="store_true",
        help="forward-header auth behind ingress-nginx and a stub auth service " "(tls-off)",
    )
    parser.add_argument(
        "--gateway",
        action="store_true",
        help="route through Envoy Gateway (HTTPRoutes) instead of ingress-nginx",
    )
    parser.add_argument(
        "--keep",
        action="store_true",
        help="skip cleanup (helm release and namespace) for debugging",
    )
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    arguments = parse_arguments(argv)
    deployment = Deployment(
        context=os.environ.get(CONTEXT_ENV, DEFAULT_CONTEXT),
        mode=Security(arguments.mode),
        # A tag per run, so cleanup removes exactly what this run loaded and never a shared tag.
        image_tag=f"{arguments.image_tag}-{secrets.token_hex(4)}",
        keep=arguments.keep,
        load=arguments.load or arguments.calibrate,
        calibrate=arguments.calibrate,
        runner=arguments.runner,
        runspecs=tuple(path.resolve() for path in arguments.runspec),
        tuning=arguments.tuning,
        tuning_host=arguments.tuning_host,
        ui=arguments.ui,
        forward_header=arguments.forward_header,
        gateway=arguments.gateway,
    )
    missing = [
        name for name in HOST_ENV_VARS if deployment.tuning_host and not os.environ.get(name)
    ]
    if missing:
        print(f"e2e-cluster: --tuning-host needs {', '.join(missing)}", file=sys.stderr)
        return 2
    if (deployment.tuning or deployment.tuning_host) and (
        deployment.runner != "kubernetes" or deployment.mode is not Security.TLS_OFF
    ):
        # The deployer needs the Job runner; kind's Prometheus cannot verify a tls target.
        print(
            "e2e-cluster: --tuning needs --runner kubernetes and tls-off",
            file=sys.stderr,
        )
        return 2
    if deployment.forward_header and (
        deployment.mode is not Security.TLS_OFF or deployment.tuning or deployment.tuning_host
    ):
        # The NetworkPolicy probe speaks plain HTTP to the api; tuning runs need no auth variant.
        print(
            "e2e-cluster: --forward-header needs tls-off, without --tuning",
            file=sys.stderr,
        )
        return 2
    if deployment.ui and not (deployment.tuning or deployment.tuning_host):
        print("e2e-cluster: --ui needs --tuning or --tuning-host", file=sys.stderr)
        return 2
    install_signal_handlers()
    stack = ExitStack()
    exit_code = 0
    try:
        run_e2e(deployment, stack)
    except E2EError as error:
        print(f"e2e-cluster: {error}", file=sys.stderr, flush=True)
        exit_code = 2
    except Exception as error:
        print(f"e2e-cluster FAILED: {error!r}", file=sys.stderr, flush=True)
        exit_code = 1
    finally:
        ignore_signals_during_cleanup()
        stack.close()
        if deployment.keep:
            print(
                f"--keep: left {deployment.namespace} in {deployment.context}. Remove with: "
                f"helm --kube-context {deployment.context} uninstall {deployment.release} "
                f"-n {deployment.namespace}; kubectl --context {deployment.context} "
                f"delete namespace {deployment.namespace}",
                flush=True,
            )
    for problem in deployment.problems:
        print(f"e2e-cluster CLEANUP PROBLEM: {problem}", file=sys.stderr, flush=True)
    if deployment.problems:
        exit_code = exit_code or 3
    if exit_code == 0:
        print(f"e2e-cluster {deployment.mode.value} passed", flush=True)
    return exit_code


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
