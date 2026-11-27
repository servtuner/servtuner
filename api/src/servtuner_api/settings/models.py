"""The api's settings: enums, defaults and the frozen dataclasses ``Settings`` is made of."""

from collections.abc import Mapping
from dataclasses import dataclass, field
from enum import StrEnum
from pathlib import Path

# Where the chart mounts Secrets for the subprocess runner (subprocess.secrets),
# which is bench's default secrets directory.
MOUNTED_SECRETS_DIR = Path("/etc/servtuner/secrets")


DEFAULT_API_PORT = 8080


DEFAULT_DATABASE_PORT = 5432


# Long enough for a multi-hour run; shorter would expire the token mid-run.
DEFAULT_RUN_TOKEN_TTL_SECONDS = 24 * 60 * 60


DEFAULT_RUNNER_POLL_SECONDS = 1.0


DEFAULT_CERT_RELOAD_SECONDS = 30.0


class ConfigError(ValueError):
    """Configuration is missing or contradictory; ``problems`` lists every one."""

    def __init__(self, problems: list[str]) -> None:
        self.problems = problems
        super().__init__("invalid configuration: " + "; ".join(problems))


class ClientAuth(StrEnum):
    """The api listener's client-certificate policy."""

    REQUIRED = "required"  # refuse a client without a valid certificate
    OPTIONAL = (
        "optional"  # verify a certificate that is sent; accept a client with none (tokens then)
    )
    NONE = "none"  # ask for no client certificate


class Hop(StrEnum):
    """The five transport hops the chart configures."""

    BENCH_API = "bench_api"
    BENCH_PROMETHEUS = "bench_prometheus"
    INGRESS_API = "ingress_api"
    OBJECT_STORE = "object_store"
    POSTGRES = "postgres"


class AntiAffinity(StrEnum):
    """Keep the bench pod off the target's nodes: always, or when the scheduler can."""

    REQUIRED = "required"
    PREFERRED = "preferred"


class RunnerKind(StrEnum):
    SUBPROCESS = "subprocess"
    KUBERNETES = "kubernetes"


class SecretStoreKind(StrEnum):
    """Where saved targets' keys live."""

    NONE = "none"  # saved targets cannot hold keys
    KUBERNETES = "kubernetes"  # Secrets in the runs namespace
    FILE = "file"  # local development: files under the data directory


class AuthMode(StrEnum):
    """Local users, or none (the explicit opt-out: nothing is checked)."""

    LOCAL = "local"
    NONE = "none"


class LoginMethod(StrEnum):
    """How users sign in under auth mode local; the order configured is the chain's order."""

    LOCAL = "local"  # a username and password, then a session cookie
    FORWARD_HEADER = "forward_header"  # a header set by the trusted proxy


class Provisioning(StrEnum):
    """What a forwarded subject with no servtuner user gets."""

    REFUSE = "refuse"
    VIEWER = "viewer"


class StorageBackend(StrEnum):
    FILE = "file"
    S3 = "s3"


class ChecksumMode(StrEnum):
    """when_required: no checksums some S3-compatible stores reject."""

    WHEN_REQUIRED = "when_required"
    WHEN_SUPPORTED = "when_supported"


class CredentialsSource(StrEnum):
    """secret: keys in AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY; ambient: the SDK's default chain."""

    SECRET = "secret"
    AMBIENT = "ambient"


class DatabaseSslMode(StrEnum):
    VERIFY_FULL = "verify-full"
    DISABLE = "disable"


# Which sslmode the Postgres hop requires, by whether it uses TLS: they may not disagree.
SSLMODE_FOR_TLS = {
    True: DatabaseSslMode.VERIFY_FULL,
    False: DatabaseSslMode.DISABLE,
}


# The hops that reach the api's own listener: their client certificate follows the
# listener's policy. The others reach a server servtuner does not run.
API_LISTENER_HOPS = frozenset({Hop.INGRESS_API, Hop.BENCH_API})


DEFAULT_S3_PART_SIZE_MIB = 16


MIN_S3_PART_SIZE_MIB = 5


DEFAULT_S3_PREFIX = "runs/"


DEFAULT_BENCH_CPU = "2"


DEFAULT_BENCH_MEMORY = "2Gi"


DEFAULT_DATA_FS_GROUP = 10001  # the images' non-root group (api/Dockerfile, bench/Dockerfile)


# Finished Jobs (and, through owner references, their ConfigMap and Secret) are
# deleted this long after they end; the api has read the final state long before.
DEFAULT_BENCH_JOB_TTL_SECONDS = 3600


# How long a bench pod may stay unstarted (unschedulable, image pull failing)
# before the run is failed with the reason.
DEFAULT_BENCH_START_TIMEOUT_SECONDS = 300


DEFAULT_SESSION_IDLE_SECONDS = 12 * 60 * 60  # 12 hours idle


DEFAULT_SESSION_ABSOLUTE_SECONDS = 7 * 24 * 60 * 60  # 7 days at most


DEFAULT_LOCKOUT_MAX_FAILURES = 5  # failed sign-ins before a lockout


DEFAULT_LOCKOUT_WINDOW_SECONDS = 15 * 60  # the counting window and the lock, 15 minutes


DEFAULT_FORWARD_USER_HEADER = "X-Forwarded-User"


@dataclass(frozen=True)
class DatabaseSettings:
    host: str
    port: int
    name: str
    user: str
    ssl_mode: DatabaseSslMode
    password: str | None = field(default=None, repr=False)


@dataclass(frozen=True)
class HopTls:
    """One hop: whether it uses TLS, and whether servtuner presents its client certificate."""

    enabled: bool
    client_cert: bool


@dataclass(frozen=True)
class TlsSettings:
    hops: Mapping[Hop, HopTls]
    # The api listener's policy (ingress_api, bench_api), meaningful when they use TLS.
    client_auth: ClientAuth
    cert_file: Path | None
    key_file: Path | None
    ca_bundle_file: Path | None  # SSL_CERT_FILE: trust for OUTBOUND connections only
    client_ca_file: Path | None  # private CA only: what the listener accepts client certs from

    def hop(self, hop: Hop) -> HopTls:
        return self.hops[hop]

    def uses_tls(self, hop: Hop) -> bool:
        return self.hops[hop].enabled

    def presents_cert(self, hop: Hop) -> bool:
        return self.hops[hop].client_cert


@dataclass(frozen=True)
class KubernetesRunnerSettings:
    """What the Kubernetes runner needs to build a bench Job (deploy/helm/servtuner/README.md)."""

    namespace: str
    bench_image: str
    service_account: str
    bench_cpu: str
    bench_memory: str
    target_anti_affinity: AntiAffinity
    job_ttl_seconds: int
    start_timeout_seconds: int
    image_pull_secrets: tuple[str, ...]
    # File backend: the claim mounted at the data directory, and whether it is
    # ReadWriteOnce, so the Job must share the api pod's node.
    data_claim: str | None
    data_fs_group: int
    node_pinned: bool
    api_component_label: tuple[str, str] | None
    api_namespace: str
    # Secrets in the runs namespace: bench's client certificate, when a hop it opens
    # presents one, and the private CA that verifies the api (with TLS).
    bench_tls_secret: str | None
    ca_secret: str | None
    # s3 with credentials source secret: the Secret and its two keys.
    s3_credentials: tuple[str, str, str] | None = None


@dataclass(frozen=True)
class S3Settings:
    """Where bundles are in an S3-compatible store; keys are <prefix><run id>/<path>."""

    endpoint: str | None  # None for AWS S3
    region: str
    bucket: str
    prefix: str
    force_path_style: bool
    checksum_mode: ChecksumMode
    credentials_source: CredentialsSource
    part_size_mib: int = DEFAULT_S3_PART_SIZE_MIB
    # presign: hand out presigned download links instead of proxying;
    # only useful when clients can reach the endpoint.
    presign: bool = False
    # Static keys, with credentials source secret only.
    access_key_id: str | None = field(default=None, repr=False)
    secret_access_key: str | None = field(default=None, repr=False)


@dataclass(frozen=True)
class SecretStoreSettings:
    kind: SecretStoreKind = SecretStoreKind.NONE
    namespace: str | None = None  # kubernetes: the runs namespace
    stored_dir: Path | None = None  # file: where keys are kept
    run_secrets_dir: Path | None = None  # where runs and checks get their files
    mounted_dir: Path = MOUNTED_SECRETS_DIR


@dataclass(frozen=True)
class ForwardHeaderSettings:
    user_header: str = DEFAULT_FORWARD_USER_HEADER
    display_name_header: str | None = None
    email_header: str | None = None
    provisioning: Provisioning = Provisioning.REFUSE


@dataclass(frozen=True)
class AuthSettings:
    """User auth. The default is local users with a session cookie, failing closed: with no
    setup token file, no first admin can be created and every route stays refused."""

    mode: AuthMode = AuthMode.LOCAL
    methods: tuple[LoginMethod, ...] = (LoginMethod.LOCAL,)
    setup_token_file: Path | None = None
    session_idle_seconds: int = DEFAULT_SESSION_IDLE_SECONDS
    session_absolute_seconds: int = DEFAULT_SESSION_ABSOLUTE_SECONDS
    is_cookie_secure: bool = True
    lockout_max_failures: int = DEFAULT_LOCKOUT_MAX_FAILURES
    lockout_window_seconds: int = DEFAULT_LOCKOUT_WINDOW_SECONDS
    forward_header: ForwardHeaderSettings = field(default_factory=ForwardHeaderSettings)

    def allows(self, method: LoginMethod) -> bool:
        return self.mode is AuthMode.LOCAL and method in self.methods


@dataclass(frozen=True)
class Settings:
    port: int
    api_url: str
    runner: RunnerKind
    bench_binary: Path | None
    engine_override: str | None  # dev and e2e only: runs bench with no real load engine
    storage_backend: StorageBackend
    data_dir: Path
    database: DatabaseSettings
    tls: TlsSettings
    kubernetes: KubernetesRunnerSettings | None = None
    s3: S3Settings | None = None
    secret_store: SecretStoreSettings = field(default_factory=SecretStoreSettings)
    prometheus_url: str | None = None
    run_token_ttl_seconds: float = DEFAULT_RUN_TOKEN_TTL_SECONDS
    runner_poll_seconds: float = DEFAULT_RUNNER_POLL_SECONDS
    cert_reload_seconds: float = DEFAULT_CERT_RELOAD_SECONDS
    tuning_namespaces: tuple[str, ...] = ()
    tuning_monitor: str = "podmonitor"
    auth: AuthSettings = field(default_factory=AuthSettings)
    # Air-gapped installs.
    image_registry: str | None = None
    offline: bool = False

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> Settings:
        from servtuner_api.settings.reader import SettingsReader  # it builds these classes

        return SettingsReader(env).read()
