"""The Kubernetes objects a managed target is made of, rendered from its spec."""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any, Literal

from servtuner_api.deployer.base import offline_env
from servtuner_api.managed_targets.spec import (
    MANAGED_TARGET_LABEL,
    SERVING_PORT,
    ManagedTargetSpec,
    MirrorModel,
    ObjectStoreModel,
    PvcModel,
    TlsTransport,
)

MANAGED_BY_LABEL = "app.kubernetes.io/managed-by"


MANAGED_BY = "servtuner"


CONTAINER = "vllm"


MODEL_CACHE_MOUNT = "/models"


TLS_MOUNT = "/etc/servtuner/tls"


# How long a model may take to load before the startup probe gives up: 180 x 10 s = 30 min;
# a failing startup probe restarts the container, so it must be generous.
STARTUP_PERIOD_SECONDS = 10


STARTUP_FAILURE_THRESHOLD = 180


# The metrics scrape the RunSpecs rely on (5 s for targets).
SCRAPE_INTERVAL = "5s"


# How Prometheus finds a managed pod: the Prometheus Operator's PodMonitor, or the
# community chart's servtuner-targets job, which keeps pods labelled servtuner.dev/scrape=true.
Monitor = Literal["podmonitor", "scrape-annotations"]


MONITORS: tuple[Monitor, ...] = ("podmonitor", "scrape-annotations")


SCRAPE_LABEL = "servtuner.dev/scrape"


# servtuner's target catalogs select job="servtuner-targets". A PodMonitor
# names its job <namespace>/<name> unless spec.jobLabel names a pod label: managed pods carry this.
SCRAPE_JOB_LABEL = "servtuner.dev/scrape-job"


SCRAPE_JOB = "servtuner-targets"


def labels_of(name: str) -> dict[str, str]:
    """The labels every object of a managed target carries; the selector uses the second."""
    return {MANAGED_BY_LABEL: MANAGED_BY, MANAGED_TARGET_LABEL: name}


def is_owned(manifest: Mapping[str, Any], name: str) -> bool:
    labels = (manifest.get("metadata") or {}).get("labels") or {}
    return all(labels.get(key) == value for key, value in labels_of(name).items())


def tls_secret_name(name: str, spec: ManagedTargetSpec) -> str | None:
    """The kubernetes.io/tls Secret vLLM serves with: cert-manager's, or the user's."""
    transport = spec.transport
    if not isinstance(transport, TlsTransport):
        return None
    return transport.secret or f"{name}-tls"


def model_cache_claim(name: str) -> str:
    return f"{name}-model-cache"


def object_store_env(source: ObjectStoreModel) -> list[dict[str, Any]]:
    """What the Run:ai Model Streamer reads (vLLM docs: models/extensions/runai_model_streamer)."""
    env: list[dict[str, Any]] = []
    if source.endpoint:
        env += [
            {"name": "AWS_ENDPOINT_URL", "value": source.endpoint},
            {"name": "RUNAI_STREAMER_S3_USE_VIRTUAL_ADDRESSING", "value": "0"},
            {"name": "AWS_EC2_METADATA_DISABLED", "value": "true"},
        ]
    if source.credentials:
        secret = source.credentials
        for variable, key in (
            ("AWS_ACCESS_KEY_ID", secret.access_key_id_key),
            ("AWS_SECRET_ACCESS_KEY", secret.secret_access_key_key),
        ):
            env.append(
                {"name": variable, "valueFrom": {"secretKeyRef": {"name": secret.name, "key": key}}}
            )
    return env


def serving_command(spec: ManagedTargetSpec) -> list[str]:
    """The container command: vLLM's CLI, or SGLang's documented server entry point."""
    if spec.server == "sglang":
        return ["python3", "-m", "sglang.launch_server"]
    return ["vllm", "serve"]


def sglang_serving_args(name: str, spec: ManagedTargetSpec, knob_args: list[str]) -> list[str]:
    """An SGLang server's args: the model, where it listens, its metrics (always on: the catalog
    and the analysis read them), TLS, then the base flags, then the knobs. SGLang never asks
    for a client certificate, so TLS is the server certificate only, reloaded on rotation."""
    args = [
        f"--model-path={spec.model}",
        "--host=0.0.0.0",
        f"--port={SERVING_PORT}",
        f"--download-dir={MODEL_CACHE_MOUNT}/hf",
        "--enable-metrics",
    ]
    if spec.served_model_name:
        args.append(f"--served-model-name={spec.served_model_name}")
    if tls_secret_name(name, spec) is not None:
        args += [
            f"--ssl-certfile={TLS_MOUNT}/tls.crt",
            f"--ssl-keyfile={TLS_MOUNT}/tls.key",
            "--enable-ssl-refresh",
        ]
    return args + spec.base_flags + knob_args


def serving_args(name: str, spec: ManagedTargetSpec, knob_args: list[str]) -> list[str]:
    """The container args: what the deployer owns, then the base flags, then the knobs."""
    if spec.server == "sglang":
        return sglang_serving_args(name, spec, knob_args)
    source = spec.model_source
    if isinstance(source, ObjectStoreModel):
        # vLLM serves the store's path; the model's own name stays what clients ask for.
        args = [
            source.uri,
            "--host=0.0.0.0",
            f"--port={SERVING_PORT}",
            "--load-format=runai_streamer",
            f"--served-model-name={spec.served_model_name or spec.model}",
        ]
    else:
        args = [
            spec.model,
            "--host=0.0.0.0",
            f"--port={SERVING_PORT}",
            f"--download-dir={MODEL_CACHE_MOUNT}/hf",
        ]
        if spec.served_model_name:
            args.append(f"--served-model-name={spec.served_model_name}")
    if tls_secret_name(name, spec) is not None:
        # Server certificate only: --ssl-cert-reqs stays 0 and no client CA is set.
        args += [f"--ssl-certfile={TLS_MOUNT}/tls.crt", f"--ssl-keyfile={TLS_MOUNT}/tls.key"]
    return args + spec.base_flags + knob_args


def _probe(scheme: str, period: int, failures: int) -> dict[str, Any]:
    return {
        "httpGet": {"path": "/health", "port": "http", "scheme": scheme},
        "periodSeconds": period,
        "failureThreshold": failures,
    }


def render_manifests(
    name: str,
    namespace: str,
    spec: ManagedTargetSpec,
    args: list[str],
    monitor: Monitor = "podmonitor",
    image_pull_secrets: tuple[str, ...] = (),
    offline: bool = False,
) -> list[dict[str, Any]]:
    """Every object of a managed target, as plain manifests, in apply order.

    ``monitor`` says how Prometheus finds the pod: a PodMonitor for the Prometheus
    Operator, or the label and annotations the community chart's servtuner-targets job reads.
    ``image_pull_secrets`` are the install's (the chart's global.imagePullSecrets), for an
    image in a private registry. ``offline`` stops FlashInfer fetching a kernel the image
    lacks: the boot fails at once and says so, instead of waiting on a network it cannot reach.
    """
    labels = labels_of(name)
    secret = tls_secret_name(name, spec)
    scheme = "HTTPS" if secret else "HTTP"

    def meta(object_name: str) -> dict[str, Any]:
        return {"name": object_name, "namespace": namespace, "labels": labels}

    env: list[dict[str, Any]] = [{"name": "HF_HOME", "value": f"{MODEL_CACHE_MOUNT}/hf"}]
    if spec.hf_token:
        env.append(
            {
                "name": "HF_TOKEN",
                "valueFrom": {
                    "secretKeyRef": {"name": spec.hf_token.name, "key": spec.hf_token.key}
                },
            }
        )
    source = spec.model_source
    if isinstance(source, MirrorModel):
        env.append({"name": "HF_ENDPOINT", "value": source.endpoint})
    elif isinstance(source, PvcModel | ObjectStoreModel):
        # Nothing comes from Hugging Face: a missing file fails the boot instead.
        env.append({"name": "HF_HUB_OFFLINE", "value": "1"})
    if isinstance(source, ObjectStoreModel):
        env += object_store_env(source)
    env += [
        {"name": key, "value": value}
        for key, value in sorted(offline_env(spec.env, offline).items())
    ]
    # A streamed model needs no volume: its config and tokenizer go to a temporary directory.
    cache_volume: dict[str, Any] = (
        {"emptyDir": {}}
        if isinstance(source, ObjectStoreModel)
        else {
            "persistentVolumeClaim": {
                "claimName": (
                    source.claim if isinstance(source, PvcModel) else model_cache_claim(name)
                )
            }
        }
    )
    mounts: list[dict[str, Any]] = [
        {"name": "model-cache", "mountPath": MODEL_CACHE_MOUNT},
        {"name": "shm", "mountPath": "/dev/shm"},  # tensor parallel workers share memory through it
    ]
    volumes: list[dict[str, Any]] = [
        {"name": "model-cache", **cache_volume},
        {
            "name": "shm",
            "emptyDir": {
                "medium": "Memory",
                **({"sizeLimit": spec.shm_size} if spec.shm_size else {}),
            },
        },
    ]
    if secret:
        mounts.append({"name": "tls", "mountPath": TLS_MOUNT, "readOnly": True})
        volumes.append({"name": "tls", "secret": {"secretName": secret}})
    requests: dict[str, str] = {"cpu": spec.cpu, "memory": spec.memory}
    limits: dict[str, str] = {"memory": spec.memory}
    if spec.gpus:
        requests["nvidia.com/gpu"] = limits["nvidia.com/gpu"] = str(spec.gpus)
    pod_spec: dict[str, Any] = {
        "containers": [
            {
                "name": CONTAINER,
                "image": spec.image,
                "command": serving_command(spec),
                "args": args,
                "ports": [{"name": "http", "containerPort": SERVING_PORT}],
                "env": env,
                "resources": {"requests": requests, "limits": limits},
                "volumeMounts": mounts,
                "startupProbe": _probe(scheme, STARTUP_PERIOD_SECONDS, STARTUP_FAILURE_THRESHOLD),
                "readinessProbe": _probe(scheme, 5, 3),
                "livenessProbe": _probe(scheme, 10, 6),
            }
        ],
        "volumes": volumes,
    }
    if image_pull_secrets:
        pod_spec["imagePullSecrets"] = [{"name": secret_name} for secret_name in image_pull_secrets]
    if spec.node_selector:
        pod_spec["nodeSelector"] = spec.node_selector
    if spec.tolerations:
        pod_spec["tolerations"] = [t.model_dump(exclude_none=True) for t in spec.tolerations]

    claims: list[dict[str, Any]] = []
    if spec.model_cache is not None:
        claim: dict[str, Any] = {
            "apiVersion": "v1",
            "kind": "PersistentVolumeClaim",
            "metadata": meta(model_cache_claim(name)),
            "spec": {
                "accessModes": ["ReadWriteOnce"],
                "resources": {"requests": {"storage": spec.model_cache.size}},
            },
        }
        if spec.model_cache.storage_class:
            claim["spec"]["storageClassName"] = spec.model_cache.storage_class
        claims.append(claim)
    endpoint: dict[str, Any] = {
        "port": "http",
        "path": "/metrics",
        "interval": SCRAPE_INTERVAL,
        "scheme": scheme.lower(),
    }
    if secret:
        # Server-authenticated only: the CA and the certificate's name, no client cert.
        endpoint["tlsConfig"] = {
            "ca": {"secret": {"name": secret, "key": "ca.crt"}},
            "serverName": f"{name}.{namespace}.svc",
        }
    manifests: list[dict[str, Any]] = []
    transport = spec.transport
    if isinstance(transport, TlsTransport) and transport.issuer is not None:
        manifests.append(
            {
                "apiVersion": "cert-manager.io/v1",
                "kind": "Certificate",
                "metadata": meta(f"{name}-tls"),
                "spec": {
                    "secretName": f"{name}-tls",
                    "dnsNames": [
                        name,
                        f"{name}.{namespace}",
                        f"{name}.{namespace}.svc",
                        f"{name}.{namespace}.svc.cluster.local",
                        "localhost",
                    ],
                    "usages": ["server auth", "digital signature", "key encipherment"],
                    "issuerRef": transport.issuer.model_dump(),
                },
            }
        )
    manifests += [
        *claims,
        {
            "apiVersion": "v1",
            "kind": "Service",
            "metadata": meta(name),
            "spec": {
                "selector": {MANAGED_TARGET_LABEL: name},
                "ports": [{"name": "http", "port": SERVING_PORT, "targetPort": "http"}],
            },
        },
    ]
    template: dict[str, Any] = {
        "metadata": {"labels": {**labels, SCRAPE_JOB_LABEL: SCRAPE_JOB}},
        "spec": pod_spec,
    }
    if monitor == "podmonitor":
        manifests.append(
            {
                "apiVersion": "monitoring.coreos.com/v1",
                "kind": "PodMonitor",
                "metadata": meta(name),
                "spec": {
                    "selector": {"matchLabels": {MANAGED_TARGET_LABEL: name}},
                    "jobLabel": SCRAPE_JOB_LABEL,
                    "podMetricsEndpoints": [endpoint],
                },
            }
        )
    else:
        # deploy/prometheus/servtuner-targets.values.yaml: scraped at 5 s, labelled namespace and
        # pod.
        template["metadata"] = {
            "labels": {**labels, SCRAPE_LABEL: "true", SCRAPE_JOB_LABEL: SCRAPE_JOB},
            "annotations": {
                "servtuner.dev/metrics-port": str(SERVING_PORT),
                "servtuner.dev/metrics-scheme": scheme.lower(),
                "servtuner.dev/metrics-path": "/metrics",
            },
        }
    manifests.append(
        {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": meta(name),
            "spec": {
                "replicas": 1,
                # Two pods must never share the GPU during a rollout.
                "strategy": {"type": "Recreate"},
                "selector": {"matchLabels": {MANAGED_TARGET_LABEL: name}},
                "template": template,
            },
        }
    )
    return manifests
