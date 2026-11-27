"""The environment variable names the api reads (the chart's environment contract), defined once."""

from servtuner_api.settings.models import Connection

# Environment variable names, defined once.
ENV_API_PORT = "SERVTUNER_API_PORT"


ENV_API_URL = "SERVTUNER_API_URL"


ENV_RUNNER = "SERVTUNER_RUNNER"


ENV_BENCH_BINARY = "SERVTUNER_BENCH_BINARY"


ENV_ENGINE_OVERRIDE = "SERVTUNER_ENGINE_OVERRIDE"


ENV_STORAGE_BACKEND = "SERVTUNER_STORAGE_BACKEND"


ENV_S3_ENDPOINT = "SERVTUNER_S3_ENDPOINT"


ENV_S3_REGION = "SERVTUNER_S3_REGION"


ENV_S3_BUCKET = "SERVTUNER_S3_BUCKET"


ENV_S3_PREFIX = "SERVTUNER_S3_PREFIX"


ENV_S3_FORCE_PATH_STYLE = "SERVTUNER_S3_FORCE_PATH_STYLE"


ENV_S3_CHECKSUM_MODE = "SERVTUNER_S3_CHECKSUM_MODE"


ENV_S3_CREDENTIALS_SOURCE = "SERVTUNER_S3_CREDENTIALS_SOURCE"


# Multipart part size for bench's uploads; the api only passes it on.
ENV_S3_PART_SIZE_MIB = "SERVTUNER_S3_PART_SIZE_MIB"


ENV_S3_PRESIGN = "SERVTUNER_S3_PRESIGN"


# The standard AWS names; the chart fills them from the Secret it names.
ENV_AWS_ACCESS_KEY_ID = "AWS_ACCESS_KEY_ID"


ENV_AWS_SECRET_ACCESS_KEY = "AWS_SECRET_ACCESS_KEY"


# Whether connections use TLS ("true" or "false"); each connection can override it.
ENV_TLS_ENABLED = "SERVTUNER_TLS_ENABLED"


# The api listener's client-certificate policy: required, optional or none.
ENV_TLS_CLIENT_AUTH = "SERVTUNER_TLS_CLIENT_AUTH"


# How the api signs in to Postgres: certificate (its client certificate; needs TLS) or password.
ENV_DATABASE_AUTH = "SERVTUNER_DATABASE_AUTH"


# servtuner 0.1's names, refused with their replacement.
LEGACY_ENV_TLS_MODE = "SERVTUNER_TLS_MODE"


ENV_TLS_CERT_FILE = "SERVTUNER_TLS_CERT_FILE"


ENV_TLS_KEY_FILE = "SERVTUNER_TLS_KEY_FILE"


ENV_TLS_CLIENT_CA_FILE = "SERVTUNER_TLS_CLIENT_CA_FILE"


ENV_CA_BUNDLE_FILE = "SSL_CERT_FILE"


# The only value bench accepts for --engine-override (bench/CLAUDE.md): an engine offering no load.
NULL_ENGINE = "null"


DEFAULT_CLIENT_CA_NAME = (
    "ca.crt"  # cert-manager puts it beside tls.crt in a kubernetes.io/tls Secret
)


PEM_CERTIFICATE_MARKER = "-----BEGIN CERTIFICATE-----"


ENV_DATABASE_HOST = "SERVTUNER_DATABASE_HOST"


ENV_DATABASE_PORT = "SERVTUNER_DATABASE_PORT"


ENV_DATABASE_NAME = "SERVTUNER_DATABASE_NAME"


ENV_DATABASE_USER = "SERVTUNER_DATABASE_USER"


ENV_DATABASE_SSLMODE = "SERVTUNER_DATABASE_SSLMODE"


ENV_DATABASE_PASSWORD = "SERVTUNER_DATABASE_PASSWORD"


# The Kubernetes runner (api/CLAUDE.md "Runners").
ENV_RUNS_NAMESPACE = "SERVTUNER_RUNS_NAMESPACE"


# Saved targets' keys and where a subprocess run or a
# target check gets them as files: a memory-backed emptyDir in the chart.
ENV_SECRET_STORE = "SERVTUNER_SECRET_STORE"


# The Prometheus a saved target's check asks (the chart's RunSpec default).
ENV_PROMETHEUS_URL = "SERVTUNER_PROMETHEUS_URL"


# Air-gapped installs: the registry the chart rewrote every image to (offered as the
# managed vLLM image registry), and whether the install has no internet access.
ENV_IMAGE_REGISTRY = "SERVTUNER_IMAGE_REGISTRY"
ENV_OFFLINE = "SERVTUNER_OFFLINE"


# Comma-separated namespaces where servtuner may deploy managed targets; the
# chart sets it from rbac.tuning.namespaces, so it matches the Roles granted.
ENV_TUNING_NAMESPACES = "SERVTUNER_TUNING_NAMESPACES"


# How Prometheus finds a managed vLLM: podmonitor (the Prometheus Operator, the default) or
# scrape-annotations (the community chart's servtuner-targets job), from tuning.monitor.
ENV_TUNING_MONITOR = "SERVTUNER_TUNING_MONITOR"


ENV_RUN_SECRETS_DIR = "SERVTUNER_RUN_SECRETS_DIR"


RUN_SECRETS_DIR_NAME = "run-secrets"


STORED_SECRETS_DIR_NAME = "secrets"


ENV_BENCH_IMAGE = "SERVTUNER_BENCH_IMAGE"


ENV_BENCH_SERVICE_ACCOUNT = "SERVTUNER_BENCH_SERVICE_ACCOUNT"


ENV_BENCH_CPU = "SERVTUNER_BENCH_CPU"


ENV_BENCH_MEMORY = "SERVTUNER_BENCH_MEMORY"


ENV_BENCH_TARGET_ANTI_AFFINITY = "SERVTUNER_BENCH_TARGET_ANTI_AFFINITY"


ENV_BENCH_JOB_TTL_SECONDS = "SERVTUNER_BENCH_JOB_TTL_SECONDS"


ENV_BENCH_START_TIMEOUT_SECONDS = "SERVTUNER_BENCH_START_TIMEOUT_SECONDS"


ENV_IMAGE_PULL_SECRETS = "SERVTUNER_IMAGE_PULL_SECRETS"


ENV_DATA_CLAIM = "SERVTUNER_DATA_CLAIM"


ENV_DATA_FS_GROUP = "SERVTUNER_DATA_FS_GROUP"


ENV_STORAGE_NODE_PINNED = "SERVTUNER_STORAGE_NODE_PINNED"


ENV_API_COMPONENT_LABEL = "SERVTUNER_API_COMPONENT_LABEL"


ENV_TLS_BENCH_SECRET = "SERVTUNER_TLS_BENCH_SECRET"


ENV_TLS_CA_SECRET = "SERVTUNER_TLS_CA_SECRET"


# The Secret holding the object store's static keys, for bench Jobs (source secret).
ENV_S3_CREDENTIALS_SECRET = "SERVTUNER_S3_CREDENTIALS_SECRET"


ENV_S3_ACCESS_KEY_KEY = "SERVTUNER_S3_ACCESS_KEY_KEY"


ENV_S3_SECRET_KEY_KEY = "SERVTUNER_S3_SECRET_KEY_KEY"


# Set by the chart from the downward API: where the api pod itself runs.
ENV_POD_NAMESPACE = "POD_NAMESPACE"


# User auth. Durations are seconds; the chart converts its values.
ENV_AUTH_MODE = "SERVTUNER_AUTH_MODE"


ENV_AUTH_METHODS = "SERVTUNER_AUTH_METHODS"


ENV_AUTH_SETUP_TOKEN_FILE = "SERVTUNER_AUTH_SETUP_TOKEN_FILE"


ENV_AUTH_SESSION_IDLE_SECONDS = "SERVTUNER_AUTH_SESSION_IDLE_SECONDS"


ENV_AUTH_SESSION_ABSOLUTE_SECONDS = "SERVTUNER_AUTH_SESSION_ABSOLUTE_SECONDS"


ENV_AUTH_COOKIE_SECURE = "SERVTUNER_AUTH_COOKIE_SECURE"


ENV_AUTH_LOCKOUT_MAX_FAILURES = "SERVTUNER_AUTH_LOCKOUT_MAX_FAILURES"


ENV_AUTH_LOCKOUT_WINDOW_SECONDS = "SERVTUNER_AUTH_LOCKOUT_WINDOW_SECONDS"


ENV_AUTH_FORWARD_USER_HEADER = "SERVTUNER_AUTH_FORWARD_USER_HEADER"


ENV_AUTH_FORWARD_DISPLAY_NAME_HEADER = "SERVTUNER_AUTH_FORWARD_DISPLAY_NAME_HEADER"


ENV_AUTH_FORWARD_EMAIL_HEADER = "SERVTUNER_AUTH_FORWARD_EMAIL_HEADER"


ENV_AUTH_FORWARD_PROVISIONING = "SERVTUNER_AUTH_FORWARD_PROVISIONING"


def connection_env_name(connection: Connection) -> str:
    """Whether one connection uses TLS, overriding SERVTUNER_TLS_ENABLED:
    SERVTUNER_TLS_CONNECTION_<NAME>, true or false."""
    return f"SERVTUNER_TLS_CONNECTION_{connection.name}"


def legacy_hop_env_name(hop: Connection) -> str:
    """servtuner 0.1's per-hop mode, refused with its replacement."""
    return f"SERVTUNER_TLS_HOP_{hop.name}"
