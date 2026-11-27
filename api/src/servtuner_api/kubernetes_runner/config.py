"""The Kubernetes runner's names, labels and mount paths, its config, and the workloads a bench pod
stays away from."""

from dataclasses import dataclass
from pathlib import PurePosixPath

from servtuner_api.settings.models import (
    Connection,
    ConnectionTls,
    KubernetesRunnerSettings,
    S3Settings,
    Settings,
)

FIELD_MANAGER = "servtuner-api"


HTTP_NOT_FOUND = 404


# Labels on everything the runner creates; the run id label finds a run's pod.
RUN_ID_LABEL = "servtuner.dev/run-id"


COMPONENT_LABELS = {"app.kubernetes.io/name": "servtuner", "app.kubernetes.io/component": "bench"}


BENCH_CONTAINER = "bench"


HOSTNAME_TOPOLOGY = "kubernetes.io/hostname"


# Where the pod finds its inputs. The secrets directory is bench's default
# (bench/internal/pipeline/secrets.go): one subdirectory per RunSpec Secret.
SPEC_DIR = PurePosixPath("/etc/servtuner/spec")


SPEC_KEY = "spec.json"


TOKEN_DIR = PurePosixPath("/etc/servtuner/run-token")


TOKEN_KEY = "token"


BENCH_TLS_DIR = PurePosixPath("/etc/servtuner/tls/bench")


CA_DIR = PurePosixPath("/etc/servtuner/tls/ca")


SECRETS_DIR = PurePosixPath("/etc/servtuner/secrets")


TMP_DIR = PurePosixPath("/tmp")  # noqa: S108 - an emptyDir under a read-only root filesystem


# 0440 (decimal 288): owner and group (the pod's fsGroup) read, nobody else.
PRIVATE_MODE = 0o440


# Indexed Jobs (engine.shards > 1): each pod's shard index, from the
# annotation the Job controller sets, reaches bench as --shard-index.
COMPLETION_INDEX_ANNOTATION = "batch.kubernetes.io/job-completion-index"


COMPLETION_INDEX_ENV = "JOB_COMPLETION_INDEX"


# Preferred spread of a sharded run's pods over nodes: below the target
# anti-affinity's weight, which matters more.
SHARD_SPREAD_WEIGHT = 50


def job_name(run_id: str) -> str:
    """Deterministic, so the Job of a run can be found again from its id."""
    return f"bench-{run_id.lower()}"


@dataclass(frozen=True)
class Workload:
    namespace: str
    selector: dict[str, str]


def workloads_in(spec: object) -> list[Workload]:
    """The workloads under test: ``target.workload`` and each upstream's."""
    if not isinstance(spec, dict):
        return []
    target = spec.get("target")
    if not isinstance(target, dict):
        return []
    candidates: list[object] = [target.get("workload")]
    upstreams = target.get("upstreams")
    if isinstance(upstreams, list):
        candidates += [
            upstream.get("workload") for upstream in upstreams if isinstance(upstream, dict)
        ]
    workloads: list[Workload] = []
    for candidate in candidates:
        if not isinstance(candidate, dict):
            continue
        namespace, selector = candidate.get("namespace"), candidate.get("selector")
        if isinstance(namespace, str) and isinstance(selector, dict) and selector:
            labels = {str(key): str(value) for key, value in selector.items()}
            workloads.append(Workload(namespace, labels))
    return workloads


@dataclass(frozen=True)
class KubernetesRunnerConfig:
    cluster: KubernetesRunnerSettings
    data_dir: PurePosixPath
    api_url: str
    api_connection: ConnectionTls
    engine_override: str | None = None  # dev and e2e only (SERVTUNER_ENGINE_OVERRIDE)
    s3: S3Settings | None = None
    object_store_connection: ConnectionTls = ConnectionTls(enabled=False, client_cert=False)
    offline: bool = False  # servtuner run --offline

    @classmethod
    def from_settings(cls, settings: Settings) -> KubernetesRunnerConfig:
        assert settings.kubernetes is not None  # Settings requires it for this runner
        return cls(
            cluster=settings.kubernetes,
            data_dir=PurePosixPath(settings.data_dir),
            api_url=settings.api_url,
            api_connection=settings.tls.connection(Connection.BENCH_API),
            engine_override=settings.engine_override,
            offline=settings.offline,
            s3=settings.s3,
            object_store_connection=settings.tls.connection(Connection.OBJECT_STORE),
        )
