"""Reading the environment into ``Settings``, collecting every problem before failing."""

from collections.abc import Mapping
from enum import StrEnum
from pathlib import Path

from servtuner_api.datadir import resolve_data_dir
from servtuner_api.settings.env import (
    DEFAULT_CLIENT_CA_NAME,
    ENV_API_COMPONENT_LABEL,
    ENV_API_PORT,
    ENV_API_URL,
    ENV_AUTH_COOKIE_SECURE,
    ENV_AUTH_FORWARD_DISPLAY_NAME_HEADER,
    ENV_AUTH_FORWARD_EMAIL_HEADER,
    ENV_AUTH_FORWARD_PROVISIONING,
    ENV_AUTH_FORWARD_USER_HEADER,
    ENV_AUTH_LOCKOUT_MAX_FAILURES,
    ENV_AUTH_LOCKOUT_WINDOW_SECONDS,
    ENV_AUTH_METHODS,
    ENV_AUTH_MODE,
    ENV_AUTH_SESSION_ABSOLUTE_SECONDS,
    ENV_AUTH_SESSION_IDLE_SECONDS,
    ENV_AUTH_SETUP_TOKEN_FILE,
    ENV_AWS_ACCESS_KEY_ID,
    ENV_AWS_SECRET_ACCESS_KEY,
    ENV_BENCH_BINARY,
    ENV_BENCH_CPU,
    ENV_BENCH_IMAGE,
    ENV_BENCH_JOB_TTL_SECONDS,
    ENV_BENCH_MEMORY,
    ENV_BENCH_SERVICE_ACCOUNT,
    ENV_BENCH_START_TIMEOUT_SECONDS,
    ENV_BENCH_TARGET_ANTI_AFFINITY,
    ENV_CA_BUNDLE_FILE,
    ENV_DATA_CLAIM,
    ENV_DATA_FS_GROUP,
    ENV_DATABASE_HOST,
    ENV_DATABASE_NAME,
    ENV_DATABASE_PASSWORD,
    ENV_DATABASE_PORT,
    ENV_DATABASE_SSLMODE,
    ENV_DATABASE_USER,
    ENV_ENGINE_OVERRIDE,
    ENV_IMAGE_PULL_SECRETS,
    ENV_IMAGE_REGISTRY,
    ENV_OFFLINE,
    ENV_POD_NAMESPACE,
    ENV_PROMETHEUS_URL,
    ENV_RUN_SECRETS_DIR,
    ENV_RUNNER,
    ENV_RUNS_NAMESPACE,
    ENV_S3_ACCESS_KEY_KEY,
    ENV_S3_BUCKET,
    ENV_S3_CHECKSUM_MODE,
    ENV_S3_CREDENTIALS_SECRET,
    ENV_S3_CREDENTIALS_SOURCE,
    ENV_S3_ENDPOINT,
    ENV_S3_FORCE_PATH_STYLE,
    ENV_S3_PART_SIZE_MIB,
    ENV_S3_PREFIX,
    ENV_S3_PRESIGN,
    ENV_S3_REGION,
    ENV_S3_SECRET_KEY_KEY,
    ENV_SECRET_STORE,
    ENV_STORAGE_BACKEND,
    ENV_STORAGE_NODE_PINNED,
    ENV_TLS_BENCH_SECRET,
    ENV_TLS_CA_SECRET,
    ENV_TLS_CERT_FILE,
    ENV_TLS_CLIENT_CA_FILE,
    ENV_TLS_KEY_FILE,
    ENV_TLS_MODE,
    ENV_TUNING_MONITOR,
    ENV_TUNING_NAMESPACES,
    NULL_ENGINE,
    PEM_CERTIFICATE_MARKER,
    RUN_SECRETS_DIR_NAME,
    STORED_SECRETS_DIR_NAME,
    hop_env_name,
)
from servtuner_api.settings.models import (
    DEFAULT_API_PORT,
    DEFAULT_BENCH_CPU,
    DEFAULT_BENCH_JOB_TTL_SECONDS,
    DEFAULT_BENCH_MEMORY,
    DEFAULT_BENCH_START_TIMEOUT_SECONDS,
    DEFAULT_DATA_FS_GROUP,
    DEFAULT_DATABASE_PORT,
    DEFAULT_FORWARD_USER_HEADER,
    DEFAULT_LOCKOUT_MAX_FAILURES,
    DEFAULT_LOCKOUT_WINDOW_SECONDS,
    DEFAULT_S3_PART_SIZE_MIB,
    DEFAULT_S3_PREFIX,
    DEFAULT_SESSION_ABSOLUTE_SECONDS,
    DEFAULT_SESSION_IDLE_SECONDS,
    MIN_S3_PART_SIZE_MIB,
    SSLMODE_FOR_MODE,
    AntiAffinity,
    AuthMode,
    AuthSettings,
    ChecksumMode,
    ConfigError,
    CredentialsSource,
    DatabaseSettings,
    DatabaseSslMode,
    ForwardHeaderSettings,
    Hop,
    KubernetesRunnerSettings,
    LoginMethod,
    Provisioning,
    RunnerKind,
    S3Settings,
    SecretStoreKind,
    SecretStoreSettings,
    Settings,
    StorageBackend,
    TlsSettings,
    TransportMode,
)


class SettingsReader:
    """Reads and cross-checks the environment, collecting every problem."""

    def __init__(self, env: Mapping[str, str]) -> None:
        self._env = env
        self._problems: list[str] = []

    def read(self) -> Settings:
        port = self._integer(ENV_API_PORT, DEFAULT_API_PORT)
        runner = self._choice(ENV_RUNNER, RunnerKind, RunnerKind.SUBPROCESS)
        backend = self._choice(ENV_STORAGE_BACKEND, StorageBackend, StorageBackend.FILE)
        tls = self._read_tls()
        database = self._read_database(tls)
        bench_binary = self._optional_path(ENV_BENCH_BINARY)
        self._check_runner(runner, bench_binary)
        kubernetes = (
            self._read_kubernetes(tls, backend) if runner is RunnerKind.KUBERNETES else None
        )
        engine_override = self._read_engine_override()
        s3 = self._read_s3(tls) if backend is StorageBackend.S3 else None
        data_dir = resolve_data_dir(self._env)
        secret_store = self._read_secret_store(data_dir)
        tuning_monitor = self._tuning_monitor()
        auth = self._read_auth()
        offline = self._boolean(ENV_OFFLINE, default=False)
        if self._problems:
            raise ConfigError(self._problems)
        return Settings(
            port=port,
            api_url=self._api_url(port, tls),
            runner=runner,
            bench_binary=bench_binary,
            engine_override=engine_override,
            storage_backend=backend,
            data_dir=data_dir,
            database=database,
            tls=tls,
            kubernetes=kubernetes,
            s3=s3,
            secret_store=secret_store,
            prometheus_url=self._env.get(ENV_PROMETHEUS_URL) or None,
            tuning_namespaces=tuple(
                name.strip()
                for name in self._env.get(ENV_TUNING_NAMESPACES, "").split(",")
                if name.strip()
            ),
            tuning_monitor=tuning_monitor,
            auth=auth,
            image_registry=self._env.get(ENV_IMAGE_REGISTRY, "").strip().rstrip("/") or None,
            offline=offline,
        )

    def _read_auth(self) -> AuthSettings:
        mode = self._choice(ENV_AUTH_MODE, AuthMode, AuthMode.LOCAL)
        methods: list[LoginMethod] = []
        for name in self._env.get(ENV_AUTH_METHODS, LoginMethod.LOCAL.value).split(","):
            name = name.strip()
            if name not in {method.value for method in LoginMethod}:
                choices = ", ".join(LoginMethod)
                self._problems.append(f"{ENV_AUTH_METHODS}: {name!r} is not one of {choices}")
            elif LoginMethod(name) not in methods:
                methods.append(LoginMethod(name))
        if mode is AuthMode.LOCAL and not methods:
            self._problems.append(f"{ENV_AUTH_METHODS} needs at least one method in mode local")
        idle = self._integer(ENV_AUTH_SESSION_IDLE_SECONDS, DEFAULT_SESSION_IDLE_SECONDS)
        absolute = self._integer(
            ENV_AUTH_SESSION_ABSOLUTE_SECONDS, DEFAULT_SESSION_ABSOLUTE_SECONDS
        )
        if idle > absolute:
            self._problems.append(
                f"{ENV_AUTH_SESSION_IDLE_SECONDS} may not exceed "
                f"{ENV_AUTH_SESSION_ABSOLUTE_SECONDS}"
            )
        return AuthSettings(
            mode=mode,
            methods=tuple(methods),
            setup_token_file=self._optional_path(ENV_AUTH_SETUP_TOKEN_FILE),
            session_idle_seconds=idle,
            session_absolute_seconds=absolute,
            is_cookie_secure=self._boolean(ENV_AUTH_COOKIE_SECURE, default=True),
            lockout_max_failures=self._integer(
                ENV_AUTH_LOCKOUT_MAX_FAILURES, DEFAULT_LOCKOUT_MAX_FAILURES
            ),
            lockout_window_seconds=self._integer(
                ENV_AUTH_LOCKOUT_WINDOW_SECONDS, DEFAULT_LOCKOUT_WINDOW_SECONDS
            ),
            forward_header=ForwardHeaderSettings(
                user_header=self._env.get(ENV_AUTH_FORWARD_USER_HEADER)
                or DEFAULT_FORWARD_USER_HEADER,
                display_name_header=self._env.get(ENV_AUTH_FORWARD_DISPLAY_NAME_HEADER) or None,
                email_header=self._env.get(ENV_AUTH_FORWARD_EMAIL_HEADER) or None,
                provisioning=self._choice(
                    ENV_AUTH_FORWARD_PROVISIONING, Provisioning, Provisioning.REFUSE
                ),
            ),
        )

    def _read_secret_store(self, data_dir: Path) -> SecretStoreSettings:
        kind = self._choice(ENV_SECRET_STORE, SecretStoreKind, SecretStoreKind.NONE)
        run_secrets_dir = (
            self._optional_path(ENV_RUN_SECRETS_DIR) or data_dir / RUN_SECRETS_DIR_NAME
        )
        if kind is SecretStoreKind.KUBERNETES:
            namespace = self._env.get(ENV_RUNS_NAMESPACE, "")
            if not namespace:
                self._problems.append(
                    f"{ENV_RUNS_NAMESPACE} is required with {ENV_SECRET_STORE}=kubernetes"
                )
            return SecretStoreSettings(kind, namespace=namespace, run_secrets_dir=run_secrets_dir)
        if kind is SecretStoreKind.FILE:
            return SecretStoreSettings(
                kind, stored_dir=data_dir / STORED_SECRETS_DIR_NAME, run_secrets_dir=run_secrets_dir
            )
        return SecretStoreSettings(kind, run_secrets_dir=run_secrets_dir)

    def _read_s3(self, tls: TlsSettings) -> S3Settings:
        endpoint = self._env.get(ENV_S3_ENDPOINT) or None
        if tls.mode_of(Hop.OBJECT_STORE) is TransportMode.MTLS:
            self._check_mtls_files(tls, [Hop.OBJECT_STORE])
            if endpoint is not None and not endpoint.startswith("https://"):
                self._problems.append(
                    f"{ENV_S3_ENDPOINT}={endpoint}: the object-store hop is mtls, so it must be https://"
                )
        elif endpoint is None or not endpoint.startswith("http://"):
            self._problems.append(
                f"{hop_env_name(Hop.OBJECT_STORE)}=platform needs an http:// {ENV_S3_ENDPOINT}"
            )
        source = self._optional_choice(ENV_S3_CREDENTIALS_SOURCE, CredentialsSource)
        if source is None:
            self._problems.append(
                f"{ENV_S3_CREDENTIALS_SOURCE} is required (secret or ambient; never inferred)"
            )
            source = CredentialsSource.AMBIENT
        access_key_id, secret_access_key = None, None
        if source is CredentialsSource.SECRET:
            access_key_id = self._env.get(ENV_AWS_ACCESS_KEY_ID) or None
            secret_access_key = self._env.get(ENV_AWS_SECRET_ACCESS_KEY) or None
            if access_key_id is None or secret_access_key is None:
                self._problems.append(
                    "credentials source secret needs "
                    f"{ENV_AWS_ACCESS_KEY_ID} and {ENV_AWS_SECRET_ACCESS_KEY}"
                )
        prefix = self._env.get(ENV_S3_PREFIX, DEFAULT_S3_PREFIX).strip("/")
        part_size_mib = self._integer(ENV_S3_PART_SIZE_MIB, DEFAULT_S3_PART_SIZE_MIB)
        if part_size_mib < MIN_S3_PART_SIZE_MIB:
            self._problems.append(
                f"{ENV_S3_PART_SIZE_MIB}={part_size_mib}: S3 needs parts of at least 5 MiB"
            )
        return S3Settings(
            endpoint=endpoint,
            region=self._required(ENV_S3_REGION),
            bucket=self._required(ENV_S3_BUCKET),
            prefix=f"{prefix}/" if prefix else "",
            force_path_style=self._boolean(ENV_S3_FORCE_PATH_STYLE, default=False),
            checksum_mode=self._choice(
                ENV_S3_CHECKSUM_MODE, ChecksumMode, ChecksumMode.WHEN_REQUIRED
            ),
            credentials_source=source,
            part_size_mib=part_size_mib,
            presign=self._boolean(ENV_S3_PRESIGN, default=False),
            access_key_id=access_key_id,
            secret_access_key=secret_access_key,
        )

    def _check_runner(self, runner: RunnerKind, bench_binary: Path | None) -> None:
        if runner is RunnerKind.KUBERNETES:
            if not self._env.get(ENV_API_URL):
                # localhost, the default, is the api pod itself: a Job cannot reach it there.
                self._problems.append(f"{ENV_API_URL} is required for the kubernetes runner")
        elif bench_binary is None:
            self._problems.append(f"{ENV_BENCH_BINARY} is required for the subprocess runner")

    def _read_kubernetes(
        self, tls: TlsSettings, backend: StorageBackend
    ) -> KubernetesRunnerSettings:
        namespace = self._required(ENV_RUNS_NAMESPACE)
        node_pinned = self._boolean(ENV_STORAGE_NODE_PINNED, default=False)
        is_file = backend is StorageBackend.FILE
        # bench presents a client certificate on each mtls hop it opens.
        api_mtls = tls.mode_of(Hop.BENCH_API) is TransportMode.MTLS or (
            backend is StorageBackend.S3 and tls.mode_of(Hop.OBJECT_STORE) is TransportMode.MTLS
        )
        return KubernetesRunnerSettings(
            namespace=namespace,
            bench_image=self._required(ENV_BENCH_IMAGE),
            service_account=self._required(ENV_BENCH_SERVICE_ACCOUNT),
            bench_cpu=self._env.get(ENV_BENCH_CPU) or DEFAULT_BENCH_CPU,
            bench_memory=self._env.get(ENV_BENCH_MEMORY) or DEFAULT_BENCH_MEMORY,
            target_anti_affinity=self._choice(
                ENV_BENCH_TARGET_ANTI_AFFINITY, AntiAffinity, AntiAffinity.REQUIRED
            ),
            job_ttl_seconds=self._integer(ENV_BENCH_JOB_TTL_SECONDS, DEFAULT_BENCH_JOB_TTL_SECONDS),
            start_timeout_seconds=self._integer(
                ENV_BENCH_START_TIMEOUT_SECONDS, DEFAULT_BENCH_START_TIMEOUT_SECONDS
            ),
            image_pull_secrets=tuple(
                name.strip()
                for name in self._env.get(ENV_IMAGE_PULL_SECRETS, "").split(",")
                if name.strip()
            ),
            data_claim=self._required(ENV_DATA_CLAIM) if is_file else None,
            data_fs_group=self._integer(ENV_DATA_FS_GROUP, DEFAULT_DATA_FS_GROUP),
            node_pinned=node_pinned,
            api_component_label=self._label(ENV_API_COMPONENT_LABEL, required=node_pinned),
            api_namespace=self._env.get(ENV_POD_NAMESPACE) or namespace,
            bench_tls_secret=self._required(ENV_TLS_BENCH_SECRET) if api_mtls else None,
            ca_secret=self._required(ENV_TLS_CA_SECRET) if api_mtls else None,
            s3_credentials=self._s3_credentials_secret() if backend is StorageBackend.S3 else None,
        )

    def _s3_credentials_secret(self) -> tuple[str, str, str] | None:
        if self._env.get(ENV_S3_CREDENTIALS_SOURCE) != CredentialsSource.SECRET.value:
            return None
        return (
            self._required(ENV_S3_CREDENTIALS_SECRET),
            self._env.get(ENV_S3_ACCESS_KEY_KEY) or "access-key",
            self._env.get(ENV_S3_SECRET_KEY_KEY) or "secret-key",
        )

    def _label(self, name: str, *, required: bool) -> tuple[str, str] | None:
        raw = self._env.get(name, "")
        if not raw:
            if required:
                self._problems.append(f"{name} is required when {ENV_STORAGE_NODE_PINNED}=true")
            return None
        key, separator, value = raw.partition("=")
        if not separator or not key or not value:
            self._problems.append(f"{name}={raw!r}: expected key=value")
            return None
        return key, value

    def _boolean(self, name: str, *, default: bool) -> bool:
        raw = self._env.get(name, "")
        if not raw:
            return default
        if raw not in ("true", "false"):
            self._problems.append(f"{name}={raw!r}: expected true or false")
            return default
        return raw == "true"

    def _read_engine_override(self) -> str | None:
        value = self._env.get(ENV_ENGINE_OVERRIDE, "")
        if value and value != NULL_ENGINE:
            self._problems.append(
                f"{ENV_ENGINE_OVERRIDE}={value!r}: the only allowed value is 'null'"
            )
        return value or None

    def _api_url(self, port: int, tls: TlsSettings) -> str:
        if self._env.get(ENV_API_URL):
            return self._env[ENV_API_URL].rstrip("/")
        scheme = "https" if tls.mode_of(Hop.BENCH_API) is TransportMode.MTLS else "http"
        return f"{scheme}://localhost:{port}"

    def _read_tls(self) -> TlsSettings:
        default_mode = self._choice(ENV_TLS_MODE, TransportMode, TransportMode.MTLS)
        hops = {hop: self._choice(hop_env_name(hop), TransportMode, default_mode) for hop in Hop}
        cert_file = self._optional_path(ENV_TLS_CERT_FILE)
        tls = TlsSettings(
            hops=hops,
            cert_file=cert_file,
            key_file=self._optional_path(ENV_TLS_KEY_FILE),
            ca_bundle_file=self._optional_path(ENV_CA_BUNDLE_FILE),
            client_ca_file=self._optional_path(ENV_TLS_CLIENT_CA_FILE)
            or (cert_file.parent / DEFAULT_CLIENT_CA_NAME if cert_file else None),
        )
        self._check_tls(tls)
        return tls

    def _check_tls(self, tls: TlsSettings) -> None:
        # The api has one listener, so the two hops that reach it cannot differ.
        if tls.mode_of(Hop.BENCH_API) is not tls.mode_of(Hop.INGRESS_API):
            self._problems.append(
                f"{hop_env_name(Hop.BENCH_API)} and {hop_env_name(Hop.INGRESS_API)} "
                "must match: the api serves both on one listener"
            )
        if (tls.cert_file is None) != (tls.key_file is None):
            self._problems.append(f"{ENV_TLS_CERT_FILE} and {ENV_TLS_KEY_FILE} go together")
        needing_certificate = [
            hop for hop in (Hop.INGRESS_API, Hop.POSTGRES) if tls.mode_of(hop) is TransportMode.MTLS
        ]
        if needing_certificate:
            self._check_mtls_files(tls, needing_certificate)
        if tls.mode_of(Hop.INGRESS_API) is TransportMode.MTLS:
            self._check_client_ca(tls.client_ca_file)
        elif tls.cert_file is not None:
            self._problems.append(f"{ENV_TLS_CERT_FILE} is set but no api hop is mtls")

    def _check_client_ca(self, client_ca_file: Path | None) -> None:
        # The combined SSL_CERT_FILE bundle includes public CAs, so using it here
        # would let any publicly issued client certificate in.
        if client_ca_file is None or not client_ca_file.is_file():
            self._problems.append(f"{ENV_TLS_CLIENT_CA_FILE}={client_ca_file}: no such file")
        elif PEM_CERTIFICATE_MARKER not in client_ca_file.read_text(errors="replace"):
            self._problems.append(
                f"{ENV_TLS_CLIENT_CA_FILE}={client_ca_file}: holds no certificates"
            )

    def _check_mtls_files(self, tls: TlsSettings, hops: list[Hop]) -> None:
        names = ", ".join(hop_env_name(hop) for hop in hops)
        for env_name, path in (
            (ENV_TLS_CERT_FILE, tls.cert_file),
            (ENV_TLS_KEY_FILE, tls.key_file),
            (ENV_CA_BUNDLE_FILE, tls.ca_bundle_file),
        ):
            if path is None:
                self._problems.append(f"{env_name} is required because {names} is mtls")
            elif not path.is_file():
                self._problems.append(f"{env_name}={path}: no such file")

    def _read_database(self, tls: TlsSettings) -> DatabaseSettings:
        mode = tls.mode_of(Hop.POSTGRES)
        sslmode = self._optional_choice(ENV_DATABASE_SSLMODE, DatabaseSslMode)
        if sslmode is None:
            self._problems.append(f"{ENV_DATABASE_SSLMODE} is required (never inferred)")
            sslmode = SSLMODE_FOR_MODE[mode]
        elif sslmode is not SSLMODE_FOR_MODE[mode]:
            self._problems.append(
                f"{ENV_DATABASE_SSLMODE}={sslmode} contradicts {hop_env_name(Hop.POSTGRES)}={mode}"
            )
        password = self._env.get(ENV_DATABASE_PASSWORD) or None
        if password is not None and mode is TransportMode.MTLS:
            self._problems.append(
                f"{ENV_DATABASE_PASSWORD} is set but the postgres hop is mtls (certificate auth)"
            )
        return DatabaseSettings(
            host=self._required(ENV_DATABASE_HOST),
            port=self._integer(ENV_DATABASE_PORT, DEFAULT_DATABASE_PORT),
            name=self._required(ENV_DATABASE_NAME),
            user=self._required(ENV_DATABASE_USER),
            ssl_mode=sslmode,
            password=password,
        )

    def _tuning_monitor(self) -> str:
        monitor = self._env.get(ENV_TUNING_MONITOR) or "podmonitor"
        if monitor not in ("podmonitor", "scrape-annotations"):
            self._problems.append(
                f"{ENV_TUNING_MONITOR}={monitor!r} is not podmonitor or scrape-annotations"
            )
        return monitor

    def _required(self, name: str) -> str:
        value = self._env.get(name, "")
        if not value:
            self._problems.append(f"{name} is required")
        return value

    def _optional_path(self, name: str) -> Path | None:
        value = self._env.get(name, "")
        return Path(value) if value else None

    def _integer(self, name: str, default: int) -> int:
        raw = self._env.get(name, "")
        if not raw:
            return default
        try:
            return int(raw)
        except ValueError:
            self._problems.append(f"{name}={raw!r}: not an integer")
            return default

    def _choice[Choice: StrEnum](self, name: str, choices: type[Choice], default: Choice) -> Choice:
        return self._optional_choice(name, choices) or default

    def _optional_choice[Choice: StrEnum](self, name: str, choices: type[Choice]) -> Choice | None:
        raw = self._env.get(name, "")
        if not raw:
            return None
        try:
            return choices(raw)
        except ValueError:
            allowed = ", ".join(choice.value for choice in choices)
            self._problems.append(f"{name}={raw!r}: expected one of {allowed}")
            return None
