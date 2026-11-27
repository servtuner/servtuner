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
    ENV_DATABASE_AUTH,
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
    ENV_TLS_CLIENT_AUTH,
    ENV_TLS_CLIENT_CA_FILE,
    ENV_TLS_ENABLED,
    ENV_TLS_KEY_FILE,
    ENV_TUNING_MONITOR,
    ENV_TUNING_NAMESPACES,
    LEGACY_ENV_TLS_MODE,
    NULL_ENGINE,
    PEM_CERTIFICATE_MARKER,
    RUN_SECRETS_DIR_NAME,
    STORED_SECRETS_DIR_NAME,
    connection_env_name,
    legacy_hop_env_name,
)
from servtuner_api.settings.models import (
    API_LISTENER_CONNECTIONS,
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
    SSLMODE_FOR_TLS,
    AntiAffinity,
    AuthMode,
    AuthSettings,
    ChecksumMode,
    ClientAuth,
    ConfigError,
    Connection,
    ConnectionTls,
    CredentialsSource,
    DatabaseAuth,
    DatabaseSettings,
    DatabaseSslMode,
    ForwardHeaderSettings,
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
        if tls.uses_tls(Connection.OBJECT_STORE):
            if tls.presents_cert(Connection.OBJECT_STORE):
                self._check_certificate_files(tls, [Connection.OBJECT_STORE])
            if endpoint is not None and not endpoint.startswith("https://"):
                self._problems.append(
                    f"{ENV_S3_ENDPOINT}={endpoint}: the object-store connection uses TLS, "
                    "so it must be https://"
                )
        elif endpoint is None or not endpoint.startswith("http://"):
            self._problems.append(
                f"{connection_env_name(Connection.OBJECT_STORE)}=false needs an http:// "
                f"{ENV_S3_ENDPOINT}"
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
        # bench's client certificate is mounted when a connection it opens presents one; the
        # private CA that verifies the api (and an object store with a client certificate) when
        # it uses TLS.
        on_s3 = backend is StorageBackend.S3
        bench_presents_cert = (
            tls.presents_cert(Connection.BENCH_API)
            or tls.presents_cert(Connection.BENCH_PROMETHEUS)
            or (on_s3 and tls.presents_cert(Connection.OBJECT_STORE))
        )
        bench_verifies_private_ca = tls.uses_tls(Connection.BENCH_API) or (
            on_s3 and tls.presents_cert(Connection.OBJECT_STORE)
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
            bench_tls_secret=self._required(ENV_TLS_BENCH_SECRET) if bench_presents_cert else None,
            ca_secret=self._required(ENV_TLS_CA_SECRET) if bench_verifies_private_ca else None,
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
        scheme = "https" if tls.uses_tls(Connection.BENCH_API) else "http"
        return f"{scheme}://localhost:{port}"

    def _read_tls(self) -> TlsSettings:
        self._refuse_legacy_tls()
        default_enabled = self._boolean(ENV_TLS_ENABLED, default=True)
        client_auth = self._choice(ENV_TLS_CLIENT_AUTH, ClientAuth, ClientAuth.REQUIRED)
        # _read_database reports a bad value; here certificate is the default, as it is there.
        database_certificate = self._env.get(ENV_DATABASE_AUTH, "") in (
            "",
            DatabaseAuth.CERTIFICATE,
        )
        connections: dict[Connection, ConnectionTls] = {}
        for connection in Connection:
            enabled = self._boolean(connection_env_name(connection), default=default_enabled)
            # Each component has one certificate and presents it with TLS, except where the server
            # will not use it: the api's listener with clientAuth none, and Postgres with password
            # auth.
            if connection in API_LISTENER_CONNECTIONS:
                client_cert = enabled and client_auth is not ClientAuth.NONE
            elif connection is Connection.POSTGRES:
                client_cert = enabled and database_certificate
            else:
                client_cert = enabled
            connections[connection] = ConnectionTls(enabled=enabled, client_cert=client_cert)
        cert_file = self._optional_path(ENV_TLS_CERT_FILE)
        tls = TlsSettings(
            connections=connections,
            client_auth=client_auth,
            cert_file=cert_file,
            key_file=self._optional_path(ENV_TLS_KEY_FILE),
            ca_bundle_file=self._optional_path(ENV_CA_BUNDLE_FILE),
            client_ca_file=self._optional_path(ENV_TLS_CLIENT_CA_FILE)
            or (cert_file.parent / DEFAULT_CLIENT_CA_NAME if cert_file else None),
        )
        self._check_tls(tls)
        return tls

    def _refuse_legacy_tls(self) -> None:
        replacement = (
            f"{ENV_TLS_ENABLED} (true or false), {ENV_TLS_CLIENT_AUTH} and the "
            "SERVTUNER_TLS_CONNECTION_<NAME> overrides"
        )
        for name in [LEGACY_ENV_TLS_MODE, *(legacy_hop_env_name(hop) for hop in Connection)]:
            if self._env.get(name):
                self._problems.append(
                    f"{name}={self._env[name]!r} was replaced in servtuner 0.2 by {replacement}"
                )

    def _check_tls(self, tls: TlsSettings) -> None:
        # The api has one listener, so the two connections that reach it cannot differ.
        if tls.uses_tls(Connection.BENCH_API) is not tls.uses_tls(Connection.INGRESS_API):
            self._problems.append(
                f"{connection_env_name(Connection.BENCH_API)} and "
                f"{connection_env_name(Connection.INGRESS_API)} must match: the api serves both on "
                "one listener"
            )
        if (tls.cert_file is None) != (tls.key_file is None):
            self._problems.append(f"{ENV_TLS_CERT_FILE} and {ENV_TLS_KEY_FILE} go together")
        # The api's certificate: served on its listener, presented to Postgres.
        needing_certificate = [
            connection
            for connection, needs in (
                (Connection.INGRESS_API, tls.uses_tls(Connection.INGRESS_API)),
                (Connection.POSTGRES, tls.presents_cert(Connection.POSTGRES)),
            )
            if needs
        ]
        if needing_certificate:
            self._check_certificate_files(tls, needing_certificate)
        if tls.uses_tls(Connection.INGRESS_API) and tls.client_auth is not ClientAuth.NONE:
            self._check_client_ca(tls.client_ca_file)
        uses_certificate = (
            tls.uses_tls(Connection.INGRESS_API)
            or tls.presents_cert(Connection.POSTGRES)
            or tls.presents_cert(Connection.OBJECT_STORE)
        )
        if tls.cert_file is not None and not uses_certificate:
            self._problems.append(
                f"{ENV_TLS_CERT_FILE} is set but no connection uses the api's certificate"
            )

    def _check_client_ca(self, client_ca_file: Path | None) -> None:
        # The combined SSL_CERT_FILE bundle includes public CAs, so using it here
        # would let any publicly issued client certificate in.
        if client_ca_file is None or not client_ca_file.is_file():
            self._problems.append(f"{ENV_TLS_CLIENT_CA_FILE}={client_ca_file}: no such file")
        elif PEM_CERTIFICATE_MARKER not in client_ca_file.read_text(errors="replace"):
            self._problems.append(
                f"{ENV_TLS_CLIENT_CA_FILE}={client_ca_file}: holds no certificates"
            )

    def _check_certificate_files(self, tls: TlsSettings, connections: list[Connection]) -> None:
        names = ", ".join(connection.value for connection in connections)
        for env_name, path in (
            (ENV_TLS_CERT_FILE, tls.cert_file),
            (ENV_TLS_KEY_FILE, tls.key_file),
            (ENV_CA_BUNDLE_FILE, tls.ca_bundle_file),
        ):
            if path is None:
                self._problems.append(
                    f"{env_name} is required because the {names} connection uses it"
                )
            elif not path.is_file():
                self._problems.append(f"{env_name}={path}: no such file")

    def _read_database(self, tls: TlsSettings) -> DatabaseSettings:
        uses_tls = tls.uses_tls(Connection.POSTGRES)
        sslmode = self._optional_choice(ENV_DATABASE_SSLMODE, DatabaseSslMode)
        if sslmode is None:
            self._problems.append(f"{ENV_DATABASE_SSLMODE} is required (never inferred)")
            sslmode = SSLMODE_FOR_TLS[uses_tls]
        elif sslmode is not SSLMODE_FOR_TLS[uses_tls]:
            self._problems.append(
                f"{ENV_DATABASE_SSLMODE}={sslmode} contradicts "
                f"{connection_env_name(Connection.POSTGRES)}={str(uses_tls).lower()}"
            )
        auth = self._choice(ENV_DATABASE_AUTH, DatabaseAuth, DatabaseAuth.CERTIFICATE)
        if auth is DatabaseAuth.CERTIFICATE and not uses_tls:
            self._problems.append(
                f"{ENV_DATABASE_AUTH}=certificate needs TLS on the postgres connection "
                f"({connection_env_name(Connection.POSTGRES)}=true); use password without TLS"
            )
        if uses_tls and auth is DatabaseAuth.PASSWORD:
            # Without a client certificate the server is still verified, against SSL_CERT_FILE.
            ca_bundle = tls.ca_bundle_file
            if ca_bundle is None:
                self._problems.append(
                    f"{ENV_CA_BUNDLE_FILE} is required because the postgres connection uses TLS"
                )
            elif not ca_bundle.is_file():
                self._problems.append(f"{ENV_CA_BUNDLE_FILE}={ca_bundle}: no such file")
        password = self._env.get(ENV_DATABASE_PASSWORD) or None
        if password is not None and auth is DatabaseAuth.CERTIFICATE:
            self._problems.append(
                f"{ENV_DATABASE_PASSWORD} is set but {ENV_DATABASE_AUTH} is certificate"
            )
        return DatabaseSettings(
            host=self._required(ENV_DATABASE_HOST),
            port=self._integer(ENV_DATABASE_PORT, DEFAULT_DATABASE_PORT),
            name=self._required(ENV_DATABASE_NAME),
            user=self._required(ENV_DATABASE_USER),
            ssl_mode=sslmode,
            auth=auth,
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
