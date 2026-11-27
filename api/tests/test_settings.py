"""Settings: the environment contract, the data-directory rule, and rejected mixes."""

from pathlib import Path

import pytest

from servtuner_api.database import build_database_url
from servtuner_api.datadir import resolve_data_dir
from servtuner_api.settings.models import (
    AntiAffinity,
    ChecksumMode,
    ClientAuth,
    ConfigError,
    DatabaseSslMode,
    Hop,
    HopTls,
    Settings,
)
from tests.support.environment import plain_env


def mtls_env(tmp_path: Path, **overrides: str) -> dict[str, str]:
    files = {"cert": "tls.crt", "key": "tls.key", "ca": "ca-bundle.crt"}
    for name in files.values():
        (tmp_path / name).touch()
    (tmp_path / "ca.crt").write_text("-----BEGIN CERTIFICATE-----\n")  # the default client CA
    return (
        plain_env(
            tmp_path,
            SERVTUNER_TLS_ENABLED="true",
            SERVTUNER_TLS_CERT_FILE=str(tmp_path / files["cert"]),
            SERVTUNER_TLS_KEY_FILE=str(tmp_path / files["key"]),
            SSL_CERT_FILE=str(tmp_path / files["ca"]),
            SERVTUNER_DATABASE_SSLMODE="verify-full",
            SERVTUNER_DATABASE_PASSWORD="",
        )
        | overrides
    )


def problems_of(env: dict[str, str]) -> str:
    with pytest.raises(ConfigError) as caught:
        Settings.from_env(env)
    return "; ".join(caught.value.problems)


def test_plain_configuration(tmp_path: Path) -> None:
    settings = Settings.from_env(plain_env(tmp_path, SERVTUNER_API_PORT="9090"))
    assert settings.port == 9090
    assert all(
        hop == HopTls(enabled=False, client_cert=False) for hop in settings.tls.hops.values()
    )
    assert settings.database.ssl_mode is DatabaseSslMode.DISABLE
    assert settings.api_url == "http://localhost:9090"
    assert settings.data_dir == tmp_path / "data"


def test_tuning_namespaces_come_from_the_chart(tmp_path: Path) -> None:
    assert Settings.from_env(plain_env(tmp_path)).tuning_namespaces == ()
    env = plain_env(tmp_path, SERVTUNER_TUNING_NAMESPACES="servtuner-tune-a, servtuner-tune-b,")
    assert Settings.from_env(env).tuning_namespaces == ("servtuner-tune-a", "servtuner-tune-b")


def test_the_tuning_monitor_is_one_of_two(tmp_path: Path) -> None:
    assert Settings.from_env(plain_env(tmp_path)).tuning_monitor == "podmonitor"
    env = plain_env(tmp_path, SERVTUNER_TUNING_MONITOR="scrape-annotations")
    assert Settings.from_env(env).tuning_monitor == "scrape-annotations"
    assert "SERVTUNER_TUNING_MONITOR" in problems_of(
        plain_env(tmp_path, SERVTUNER_TUNING_MONITOR="servicemonitor")
    )


def test_mtls_configuration(tmp_path: Path) -> None:
    settings = Settings.from_env(
        mtls_env(tmp_path, SERVTUNER_API_URL="https://api.example.invalid/")
    )
    # TLS everywhere, client certificates required by the listener and presented on every hop.
    assert all(hop == HopTls(enabled=True, client_cert=True) for hop in settings.tls.hops.values())
    assert settings.tls.client_auth is ClientAuth.REQUIRED
    assert settings.api_url == "https://api.example.invalid"


def test_hop_overrides_the_default(tmp_path: Path) -> None:
    env = plain_env(tmp_path, SERVTUNER_TLS_HOP_BENCH_PROMETHEUS_ENABLED="true")
    settings = Settings.from_env(env)
    assert settings.tls.hop(Hop.BENCH_PROMETHEUS) == HopTls(enabled=True, client_cert=True)
    assert settings.tls.hop(Hop.POSTGRES) == HopTls(enabled=False, client_cert=False)
    env |= {"SERVTUNER_TLS_HOP_BENCH_PROMETHEUS_CLIENT_CERT": "false"}
    assert Settings.from_env(env).tls.hop(Hop.BENCH_PROMETHEUS) == HopTls(
        enabled=True, client_cert=False
    )


def test_tls_defaults_to_on_and_then_demands_certificates(tmp_path: Path) -> None:
    env = plain_env(tmp_path)
    del env["SERVTUNER_TLS_ENABLED"]
    assert "SERVTUNER_TLS_CERT_FILE is required" in problems_of(env)


def test_certificates_no_hop_uses_are_rejected(tmp_path: Path) -> None:
    env = plain_env(
        tmp_path, SERVTUNER_TLS_CERT_FILE=str(tmp_path / "x"), SERVTUNER_TLS_KEY_FILE="y"
    )
    assert "no hop uses the api's certificate" in problems_of(env)


@pytest.mark.parametrize("policy", list(ClientAuth))
def test_the_listener_client_auth_policy(tmp_path: Path, policy: ClientAuth) -> None:
    settings = Settings.from_env(mtls_env(tmp_path, SERVTUNER_TLS_CLIENT_AUTH=policy.value))
    assert settings.tls.client_auth is policy
    # bench presents its certificate to the api exactly when the policy asks for one.
    presents = policy is not ClientAuth.NONE
    assert settings.tls.hop(Hop.BENCH_API) == HopTls(enabled=True, client_cert=presents)


def test_client_auth_none_needs_no_client_ca(tmp_path: Path) -> None:
    required, none = mtls_env(tmp_path), mtls_env(tmp_path, SERVTUNER_TLS_CLIENT_AUTH="none")
    (tmp_path / "ca.crt").unlink()
    assert "SERVTUNER_TLS_CLIENT_CA_FILE" in problems_of(required)
    Settings.from_env(none)


def test_the_listener_hops_take_no_client_cert_override(tmp_path: Path) -> None:
    env = mtls_env(tmp_path, SERVTUNER_TLS_HOP_BENCH_API_CLIENT_CERT="false")
    assert "SERVTUNER_TLS_CLIENT_AUTH" in problems_of(env)


def test_a_client_cert_needs_tls(tmp_path: Path) -> None:
    env = plain_env(tmp_path, SERVTUNER_TLS_HOP_POSTGRES_CLIENT_CERT="true")
    assert "SERVTUNER_TLS_HOP_POSTGRES_ENABLED=true" in problems_of(env)


@pytest.mark.parametrize(
    ("name", "value"),
    [("SERVTUNER_TLS_MODE", "mtls"), ("SERVTUNER_TLS_HOP_POSTGRES", "platform")],
)
def test_servtuner_0_1_names_are_refused_with_their_replacement(
    tmp_path: Path, name: str, value: str
) -> None:
    problems = problems_of(plain_env(tmp_path, **{name: value}))
    assert f"{name}={value!r} was replaced in servtuner 0.2" in problems
    assert "SERVTUNER_TLS_ENABLED" in problems


def test_postgres_tls_without_a_client_certificate_takes_a_password(tmp_path: Path) -> None:
    env = mtls_env(
        tmp_path,
        SERVTUNER_TLS_HOP_POSTGRES_CLIENT_CERT="false",
        SERVTUNER_DATABASE_PASSWORD="not-a-real-password",
    )
    settings = Settings.from_env(env)
    assert settings.tls.hop(Hop.POSTGRES) == HopTls(enabled=True, client_cert=False)
    url = build_database_url(settings.database, settings.tls)
    assert url.query["sslmode"] == "verify-full" and "sslrootcert" in url.query
    assert "sslcert" not in url.query and "sslkey" not in url.query


def test_postgres_sslmode_must_agree_with_the_hop(tmp_path: Path) -> None:
    assert "contradicts" in problems_of(
        plain_env(tmp_path, SERVTUNER_DATABASE_SSLMODE="verify-full")
    )
    assert "contradicts" in problems_of(mtls_env(tmp_path, SERVTUNER_DATABASE_SSLMODE="disable"))


def test_sslmode_is_never_inferred(tmp_path: Path) -> None:
    env = plain_env(tmp_path)
    del env["SERVTUNER_DATABASE_SSLMODE"]
    assert "SERVTUNER_DATABASE_SSLMODE is required" in problems_of(env)


def test_password_with_a_client_certificate_is_rejected(tmp_path: Path) -> None:
    assert "certificate auth" in problems_of(
        mtls_env(tmp_path, SERVTUNER_DATABASE_PASSWORD="secret")
    )


def test_one_listener_cannot_serve_two_modes(tmp_path: Path) -> None:
    env = mtls_env(tmp_path, SERVTUNER_TLS_HOP_INGRESS_API_ENABLED="false")
    assert "one listener" in problems_of(env)


def test_missing_certificate_files_are_reported(tmp_path: Path) -> None:
    env = mtls_env(tmp_path, SERVTUNER_TLS_CERT_FILE=str(tmp_path / "missing.crt"))
    assert "missing.crt: no such file" in problems_of(env)


def test_every_problem_is_listed_at_once(tmp_path: Path) -> None:
    env = plain_env(tmp_path, SERVTUNER_API_PORT="x", SERVTUNER_RUNNER="nomad")
    del env["SERVTUNER_DATABASE_HOST"]
    problems = problems_of(env)
    assert "SERVTUNER_API_PORT" in problems and "SERVTUNER_RUNNER" in problems
    assert "SERVTUNER_DATABASE_HOST is required" in problems


def kubernetes_env(tmp_path: Path, **overrides: str) -> dict[str, str]:
    return plain_env(
        tmp_path,
        SERVTUNER_RUNNER="kubernetes",
        SERVTUNER_API_URL="http://servtuner-api.servtuner.svc:8080",
        SERVTUNER_RUNS_NAMESPACE="servtuner",
        SERVTUNER_BENCH_IMAGE="servtuner-bench:1.0.0",
        SERVTUNER_BENCH_SERVICE_ACCOUNT="servtuner-bench",
        SERVTUNER_DATA_CLAIM="servtuner-data",
        POD_NAMESPACE="servtuner-system",
        **overrides,
    )


def test_kubernetes_runner_configuration(tmp_path: Path) -> None:
    settings = Settings.from_env(
        kubernetes_env(
            tmp_path,
            SERVTUNER_STORAGE_NODE_PINNED="true",
            SERVTUNER_API_COMPONENT_LABEL="app.kubernetes.io/component=api",
            SERVTUNER_IMAGE_PULL_SECRETS="one, two",
            SERVTUNER_BENCH_TARGET_ANTI_AFFINITY="preferred",
        )
    )
    cluster = settings.kubernetes
    assert cluster is not None
    assert (cluster.namespace, cluster.bench_image, cluster.data_claim) == (
        "servtuner",
        "servtuner-bench:1.0.0",
        "servtuner-data",
    )
    assert (cluster.bench_cpu, cluster.bench_memory) == ("2", "2Gi")
    assert cluster.target_anti_affinity is AntiAffinity.PREFERRED
    assert cluster.node_pinned and cluster.api_component_label == (
        "app.kubernetes.io/component",
        "api",
    )
    assert cluster.api_namespace == "servtuner-system"
    assert cluster.image_pull_secrets == ("one", "two")
    assert cluster.bench_tls_secret is None and cluster.ca_secret is None  # platform mode
    assert settings.api_url == "http://servtuner-api.servtuner.svc:8080"


def test_kubernetes_runner_needs_its_contract(tmp_path: Path) -> None:
    env = kubernetes_env(tmp_path, SERVTUNER_STORAGE_NODE_PINNED="true")
    for name in ("SERVTUNER_API_URL", "SERVTUNER_BENCH_IMAGE", "SERVTUNER_DATA_CLAIM"):
        del env[name]
    problems = problems_of(env)
    for name in (
        "SERVTUNER_API_URL",
        "SERVTUNER_BENCH_IMAGE",
        "SERVTUNER_DATA_CLAIM",
        "SERVTUNER_API_COMPONENT_LABEL",
    ):
        assert f"{name} is required" in problems


def test_kubernetes_runner_in_mtls_needs_the_bench_and_ca_secrets(tmp_path: Path) -> None:
    env = (
        mtls_env(tmp_path)
        | kubernetes_env(tmp_path, SERVTUNER_TLS_ENABLED="true")
        | {
            "SERVTUNER_API_URL": "https://servtuner-api.servtuner.svc:8080",
            "SERVTUNER_DATABASE_SSLMODE": "verify-full",
            "SERVTUNER_DATABASE_PASSWORD": "",
            "SERVTUNER_TLS_CERT_FILE": str(tmp_path / "tls.crt"),
            "SERVTUNER_TLS_KEY_FILE": str(tmp_path / "tls.key"),
            "SSL_CERT_FILE": str(tmp_path / "ca-bundle.crt"),
        }
    )
    problems = problems_of(env)
    assert "SERVTUNER_TLS_BENCH_SECRET is required" in problems
    assert "SERVTUNER_TLS_CA_SECRET is required" in problems
    env |= {
        "SERVTUNER_TLS_BENCH_SECRET": "servtuner-bench-tls",
        "SERVTUNER_TLS_CA_SECRET": "servtuner-api-tls",
    }
    cluster = Settings.from_env(env).kubernetes
    assert cluster is not None
    assert (cluster.bench_tls_secret, cluster.ca_secret) == (
        "servtuner-bench-tls",
        "servtuner-api-tls",
    )


def test_kubernetes_runner_rejects_malformed_values(tmp_path: Path) -> None:
    problems = problems_of(
        kubernetes_env(
            tmp_path,
            SERVTUNER_STORAGE_NODE_PINNED="yes",
            SERVTUNER_API_COMPONENT_LABEL="no-equals-sign",
            SERVTUNER_BENCH_TARGET_ANTI_AFFINITY="sometimes",
            SERVTUNER_BENCH_JOB_TTL_SECONDS="soon",
        )
    )
    for fragment in (
        "SERVTUNER_STORAGE_NODE_PINNED='yes'",
        "SERVTUNER_API_COMPONENT_LABEL='no-equals-sign'",
        "SERVTUNER_BENCH_TARGET_ANTI_AFFINITY='sometimes'",
        "SERVTUNER_BENCH_JOB_TTL_SECONDS='soon'",
    ):
        assert fragment in problems


def test_subprocess_runner_needs_a_bench_binary(tmp_path: Path) -> None:
    env = plain_env(tmp_path)
    del env["SERVTUNER_BENCH_BINARY"]
    assert "SERVTUNER_BENCH_BINARY is required" in problems_of(env)


def test_database_password_is_not_in_repr(tmp_path: Path) -> None:
    assert "not-a-real-password" not in repr(Settings.from_env(plain_env(tmp_path)))


def test_data_dir_resolution_order(tmp_path: Path) -> None:
    home = tmp_path / "home"
    assert resolve_data_dir({}, home) == home / ".local" / "share" / "servtuner"
    assert resolve_data_dir({"XDG_DATA_HOME": "/xdg"}, home) == Path("/xdg/servtuner")
    both = {"XDG_DATA_HOME": "/xdg", "SERVTUNER_DATA_DIR": "/explicit"}
    assert resolve_data_dir(both, home) == Path("/explicit")


def test_client_ca_defaults_beside_the_certificate_and_must_hold_certificates(
    tmp_path: Path,
) -> None:
    env = mtls_env(tmp_path)
    assert Settings.from_env(env).tls.client_ca_file == tmp_path / "ca.crt"
    (tmp_path / "ca.crt").write_text("")
    assert "holds no certificates" in problems_of(env)
    (tmp_path / "ca.crt").unlink()
    assert "ca.crt: no such file" in problems_of(env)


def test_engine_override_defaults_off_and_accepts_only_null(tmp_path: Path) -> None:
    assert Settings.from_env(plain_env(tmp_path)).engine_override is None
    assert (
        Settings.from_env(plain_env(tmp_path, SERVTUNER_ENGINE_OVERRIDE="null")).engine_override
        == "null"
    )
    assert "only allowed value is 'null'" in problems_of(
        plain_env(tmp_path, SERVTUNER_ENGINE_OVERRIDE="loadgen")
    )


def test_air_gapped_settings(tmp_path: Path) -> None:
    default = Settings.from_env(plain_env(tmp_path))
    assert (default.offline, default.image_registry) == (False, None)
    set_ = Settings.from_env(
        plain_env(tmp_path, SERVTUNER_OFFLINE="true", SERVTUNER_IMAGE_REGISTRY="reg.example:5000/")
    )
    assert (set_.offline, set_.image_registry) == (True, "reg.example:5000")
    assert "expected true or false" in problems_of(plain_env(tmp_path, SERVTUNER_OFFLINE="yes"))


def s3_env(tmp_path: Path, **overrides: str) -> dict[str, str]:
    base = plain_env(
        tmp_path,
        SERVTUNER_STORAGE_BACKEND="s3",
        SERVTUNER_S3_ENDPOINT="http://rustfs.example.invalid:9000",
        SERVTUNER_S3_REGION="us-east-1",
        SERVTUNER_S3_BUCKET="servtuner-runs",
        SERVTUNER_S3_FORCE_PATH_STYLE="true",
        SERVTUNER_S3_CREDENTIALS_SOURCE="secret",
        AWS_ACCESS_KEY_ID="access",
        AWS_SECRET_ACCESS_KEY="not-a-real-secret",
    )
    return base | overrides


def test_s3_configuration(tmp_path: Path) -> None:
    s3 = Settings.from_env(s3_env(tmp_path)).s3
    assert s3 is not None
    assert (s3.bucket, s3.prefix, s3.force_path_style) == ("servtuner-runs", "runs/", True)
    assert s3.checksum_mode is ChecksumMode.WHEN_REQUIRED
    assert s3.part_size_mib == 16
    larger = Settings.from_env(s3_env(tmp_path, SERVTUNER_S3_PART_SIZE_MIB="64")).s3
    assert larger is not None and larger.part_size_mib == 64
    assert s3.access_key_id == "access"
    assert "not-a-real-secret" not in repr(s3)


def test_s3_needs_its_contract_and_never_infers(tmp_path: Path) -> None:
    env = s3_env(tmp_path, SERVTUNER_S3_CREDENTIALS_SOURCE="")
    del env["SERVTUNER_S3_BUCKET"], env["AWS_SECRET_ACCESS_KEY"]
    env["SERVTUNER_S3_CREDENTIALS_SOURCE"] = "secret"
    problems = problems_of(env)
    assert "SERVTUNER_S3_BUCKET is required" in problems
    assert "credentials source secret needs" in problems
    assert "is required (secret or ambient" in problems_of(
        s3_env(tmp_path, SERVTUNER_S3_CREDENTIALS_SOURCE="")
    )
    assert "S3 needs parts of at least 5 MiB" in problems_of(
        s3_env(tmp_path, SERVTUNER_S3_PART_SIZE_MIB="4")
    )
    # platform means plain HTTP, so an https endpoint contradicts the hop mode.
    assert "needs an http://" in problems_of(
        s3_env(tmp_path, SERVTUNER_S3_ENDPOINT="https://s3.example.invalid")
    )


def test_ambient_s3_has_no_keys(tmp_path: Path) -> None:
    env = s3_env(tmp_path, SERVTUNER_S3_CREDENTIALS_SOURCE="ambient")
    del env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"]
    s3 = Settings.from_env(env).s3
    assert s3 is not None and s3.access_key_id is None
