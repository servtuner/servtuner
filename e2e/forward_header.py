"""Forward-header auth on kind: the api behind
ingress-nginx's ``auth-url`` or Envoy Gateway's external authorization, with a stub auth service
standing in for oauth2-proxy.

The stub "signs in" whoever names themselves in ``X-E2E-Login`` and answers with
``X-Auth-Request-User``, which ingress-nginx copies onto the request it forwards. It runs in the
api image (stdlib Python only), so the run builds nothing extra. Also checked here: that
ingress-nginx overwrites a client's own copy of the user header, and whether kind's CNI enforces
the chart's NetworkPolicy.
"""

import json
import subprocess
from collections.abc import Callable
from pathlib import Path

import httpx2
from common import Security, check, exercise_run, wait_for

LOGIN_HEADER = "X-E2E-Login"  # what the stub reads: who is "signing in"
USER_HEADER = "X-Auth-Request-User"  # what the stub answers and the api trusts
STUB_NAME = "e2e-forward-auth"
STUB_PORT = 8080
ADMIN_SUBJECT = "e2e-admin@example.test"
VIEWER_SUBJECT = "CN=E2E Viewer,O=Example"  # a DN, as a client-certificate ingress would send
SETUP_HEADER = "X-Servtuner-Setup-Token"
PROBE_POD = "e2e-networkpolicy-probe"
PROBE_TIMEOUT_SECONDS = 5
PROBE_WAIT = "120s"

STUB_SCRIPT = f"""
import http.server


class Handler(http.server.BaseHTTPRequestHandler):
    def check(self):
        # ingress-nginx asks with GET; Envoy asks with the request's own method and body.
        self.rfile.read(int(self.headers.get("Content-Length") or 0))
        login = self.headers.get("{LOGIN_HEADER}")
        self.send_response(200 if login else 401)
        if login:
            self.send_header("{USER_HEADER}", login)
        self.end_headers()

    do_GET = do_HEAD = do_POST = do_PUT = do_PATCH = do_DELETE = check

    def log_message(self, *arguments):
        pass


http.server.ThreadingHTTPServer(("0.0.0.0", {STUB_PORT}), Handler).serve_forever()
"""


def stub_manifests(namespace: str, image: str) -> str:
    """The stub's ConfigMap, Deployment and Service, as a kubectl apply document."""
    labels = {"app.kubernetes.io/name": STUB_NAME}
    objects = [
        {
            "apiVersion": "v1",
            "kind": "ConfigMap",
            "metadata": {"name": STUB_NAME, "namespace": namespace},
            "data": {"stub.py": STUB_SCRIPT},
        },
        {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {"name": STUB_NAME, "namespace": namespace},
            "spec": {
                "replicas": 1,
                "selector": {"matchLabels": labels},
                "template": {
                    "metadata": {"labels": labels},
                    "spec": {
                        "containers": [
                            {
                                "name": "stub",
                                "image": image,
                                "imagePullPolicy": "IfNotPresent",
                                "command": ["python", "/stub/stub.py"],
                                "ports": [{"containerPort": STUB_PORT}],
                                "readinessProbe": {
                                    "tcpSocket": {"port": STUB_PORT},
                                    "periodSeconds": 2,
                                },
                                "volumeMounts": [{"name": "stub", "mountPath": "/stub"}],
                                "securityContext": {
                                    "allowPrivilegeEscalation": False,
                                    "readOnlyRootFilesystem": True,
                                    "capabilities": {"drop": ["ALL"]},
                                },
                            }
                        ],
                        "volumes": [{"name": "stub", "configMap": {"name": STUB_NAME}}],
                    },
                },
            },
        },
        {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": {"name": STUB_NAME, "namespace": namespace},
            "spec": {
                "selector": labels,
                "ports": [{"port": STUB_PORT, "targetPort": STUB_PORT}],
            },
        },
    ]
    return "\n---\n".join(json.dumps(item) for item in objects)


def chart_values(namespace: str, *, is_gateway: bool) -> dict[str, object]:
    """Forward-header auth, and the ingress or the gateway asking the stub before every request."""
    values: dict[str, object] = {
        "auth": {
            "mode": "local",
            "methods": ["forward_header"],
            "forwardHeader": {"userHeader": USER_HEADER},
            # kind is reached over plain HTTP through a port-forward.
            "cookie": {"secure": False},
        }
    }
    if is_gateway:
        stub = {"name": STUB_NAME, "port": STUB_PORT}
        # Envoy sends an HTTP auth service only a few headers unless told (gateway-api.md).
        values["gateway"] = {
            "envoy": {
                "extAuth": {
                    "enabled": True,
                    "service": stub,
                    "headersToExtAuth": [LOGIN_HEADER],
                }
            }
        }
    else:
        values["ingress"] = {
            "annotations": {
                "nginx.ingress.kubernetes.io/auth-url": (
                    f"http://{STUB_NAME}.{namespace}.svc.cluster.local:{STUB_PORT}/"
                ),
                "nginx.ingress.kubernetes.io/auth-response-headers": USER_HEADER,
            }
        }
    return values


def write_chart_values(namespace: str, path: Path, *, is_gateway: bool) -> Path:
    path.write_text(json.dumps(chart_values(namespace, is_gateway=is_gateway)))  # JSON is YAML
    return path


def as_subject(subject: str) -> dict[str, str]:
    return {LOGIN_HEADER: subject}


def exercise_forward_header(
    api: httpx2.Client,
    setup_token: str,
    mode: Security,
    check_bundle: Callable[[str], None],
) -> str:
    """Setup, refusals, the overwritten header, a linked viewer and one run; returns the run."""
    anonymous = api.get("/runs", headers={LOGIN_HEADER: ""})  # the stub signs in nobody
    check(
        anonymous.status_code in (401, 403),
        f"the proxy refuses a request with no login ({anonymous.status_code})",
    )

    before = api.get("/runs", headers=as_subject(ADMIN_SUBJECT))
    check(
        before.status_code == 401 and before.json()["detail"]["code"] == "setup_required",
        f"through the proxy, the api asks for setup ({before.status_code})",
    )
    created = api.post(
        "/auth/setup",
        json={},
        headers={**as_subject(ADMIN_SUBJECT), SETUP_HEADER: setup_token},
    )
    check(
        created.status_code == 201,
        f"setup made the forwarded subject admin ({created.status_code})",
    )

    forged = api.get(
        "/auth/me",
        headers={**as_subject(ADMIN_SUBJECT), USER_HEADER: "intruder@example.test"},
    )
    check(
        forged.status_code == 200 and forged.json()["role"] == "admin",
        "the proxy overwrites a client's own user header (the forged one never arrives)",
    )

    stranger = api.get("/runs", headers=as_subject(VIEWER_SUBJECT))
    check(
        stranger.status_code == 403 and stranger.json()["detail"]["code"] == "unknown_identity",
        f"an unknown subject is refused ({stranger.status_code})",
    )
    linked = api.post(
        "/users",
        json={"username": "e2e-viewer", "role": "viewer", "subjects": [VIEWER_SUBJECT]},
        headers=as_subject(ADMIN_SUBJECT),
    )
    check(
        linked.status_code == 201,
        f"the admin links the DN as a viewer ({linked.status_code})",
    )
    viewer = as_subject(VIEWER_SUBJECT)
    check(api.get("/runs", headers=viewer).status_code == 200, "the viewer reads runs")
    refused = api.post(
        "/runs", content=b"{}", headers={**viewer, "Content-Type": "application/json"}
    )
    check(
        refused.status_code == 403,
        f"the viewer cannot create a run ({refused.status_code})",
    )

    run_id = exercise_run(api, mode, check_bundle)
    run = api.get(f"/runs/{run_id}").json()
    check(run.get("created_by_username") is not None, "the run records who created it")
    return run_id


def probe_network_policy(
    kubectl: Callable[..., subprocess.CompletedProcess[str]],
    namespace: str,
    image: str,
    api_url: str,
) -> bool:
    """Whether a pod the chart's NetworkPolicy does not admit can reach the api. Informational:
    kind's CNI may not enforce NetworkPolicy at all."""
    script = (
        "import sys, urllib.request\n"
        "try:\n"
        f"    urllib.request.urlopen({api_url + '/healthz'!r}, timeout={PROBE_TIMEOUT_SECONDS})\n"
        "except Exception as error:\n"
        "    print('blocked:', error); sys.exit(1)\n"
        "print('reached')\n"
    )
    kubectl(
        "run",
        PROBE_POD,
        "--namespace",
        namespace,
        f"--image={image}",
        "--image-pull-policy=IfNotPresent",
        "--restart=Never",
        "--command",
        "--",
        "python",
        "-c",
        script,
    )
    try:
        wait_for(
            "the NetworkPolicy probe to finish",
            lambda: kubectl(
                "get",
                "pod",
                PROBE_POD,
                "--namespace",
                namespace,
                "-o",
                "jsonpath={.status.phase}",
            ).stdout.strip()
            in ("Succeeded", "Failed"),
            120.0,
        )
        output = kubectl("logs", PROBE_POD, "--namespace", namespace).stdout.strip()
    finally:
        kubectl("delete", "pod", PROBE_POD, "--namespace", namespace, "--wait=false")
    is_reached = output.startswith("reached")
    print(
        "info NetworkPolicy "
        + ("NOT enforced" if is_reached else "enforced")
        + f" on this cluster's CNI: an unlisted pod {output}",
        flush=True,
    )
    return not is_reached
