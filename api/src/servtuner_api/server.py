"""Process entry point: migrate, build the app, serve over mTLS or plain HTTP."""

import logging
import os
import sys
from collections.abc import Mapping, Sequence
from typing import Any

import uvicorn
from fastapi import FastAPI

from servtuner_api.app import create_app
from servtuner_api.auth.cli import run_admin_command
from servtuner_api.auth.service import AuthService
from servtuner_api.auth.store import AuthStore
from servtuner_api.bundles import BundleStore, FileBundleStore, S3BundleStore
from servtuner_api.comparer import Comparer
from servtuner_api.database import (
    build_database_url,
    create_database_engine,
    create_session_factory,
    upgrade_database,
)
from servtuner_api.deployer.base import bench_vllm_args
from servtuner_api.deployer.kubernetes import Deployer, LightkubeManagedCluster
from servtuner_api.deployer.launcher import LauncherDeployer
from servtuner_api.kubernetes_runner.cluster import LightkubeCluster
from servtuner_api.kubernetes_runner.config import KubernetesRunnerConfig
from servtuner_api.kubernetes_runner.runner import KubernetesJobRunner
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.planner import Planner
from servtuner_api.records.repository import RunRepository
from servtuner_api.runner import Runner, SubprocessRunner, SubprocessRunnerConfig
from servtuner_api.secret_store import FileSecretStore, KubernetesSecretStore, SecretStore
from servtuner_api.service import RunService
from servtuner_api.settings.models import (
    ConfigError,
    Hop,
    RunnerKind,
    SecretStoreKind,
    Settings,
    TransportMode,
)
from servtuner_api.specs import SpecValidator
from servtuner_api.targets.checker import TargetChecker
from servtuner_api.targets.service import TargetService
from servtuner_api.tls import CertificateReloader, reloader_for, uvicorn_ssl_options
from servtuner_api.tuning.proposer import Proposer

LISTEN_HOST = "0.0.0.0"  # noqa: S104 - the pod's only listener; the chart's Service fronts it


def build_runner(settings: Settings, secret_store: SecretStore | None = None) -> Runner:
    """The one place that knows which runner is in use (api/CLAUDE.md)."""
    if settings.runner is RunnerKind.KUBERNETES:
        config = KubernetesRunnerConfig.from_settings(settings)
        return KubernetesJobRunner(config, LightkubeCluster(config.cluster.namespace))
    return SubprocessRunner(SubprocessRunnerConfig.from_settings(settings), secret_store)


def build_secret_store(settings: Settings) -> SecretStore | None:
    """Where saved targets' keys live; None when not configured."""
    configured = settings.secret_store
    if configured.kind is SecretStoreKind.KUBERNETES:
        assert configured.namespace  # Settings require it
        return KubernetesSecretStore(configured.namespace)
    if configured.kind is SecretStoreKind.FILE:
        assert configured.stored_dir is not None
        return FileSecretStore(configured.stored_dir)
    return None


def default_prometheus(settings: Settings) -> dict[str, Any] | None:
    """The Prometheus a saved target's check asks, as a RunSpec prometheus block.

    mtls needs bench's client certificate Secret, which only the Kubernetes
    runner names; without it the check skips the metrics checks and says so.
    """
    if settings.prometheus_url is None:
        return None
    if settings.tls.mode_of(Hop.BENCH_PROMETHEUS) is TransportMode.PLATFORM:
        return {"url": settings.prometheus_url, "tls": {"mode": "platform"}}
    secret = settings.kubernetes.bench_tls_secret if settings.kubernetes else None
    if secret is None:
        return None
    return {"url": settings.prometheus_url, "tls": {"mode": "mtls", "secretRef": {"name": secret}}}


def checked_from(settings: Settings, env: Mapping[str, str]) -> str:
    """Where a check runs, for its result."""
    pod = env.get("POD_NAME")
    where = f"the api pod {pod}" if pod else "the api process"
    if settings.kubernetes is not None:
        return f"{where}; runs use Job pods in namespace {settings.kubernetes.namespace}"
    return where


def build_store(settings: Settings) -> BundleStore:
    """The bundle store bench writes to: the data directory, or S3."""
    if settings.s3 is not None:
        return S3BundleStore(settings.s3, settings.tls.mode_of(Hop.OBJECT_STORE), settings.tls)
    return FileBundleStore(settings.data_dir)


def build_app(settings: Settings, *, migrate: bool = True) -> FastAPI:
    engine = create_database_engine(build_database_url(settings.database, settings.tls))
    if migrate:
        upgrade_database(engine)

    def build_auth() -> AuthService:
        return AuthService(settings.auth, AuthStore(create_session_factory(engine)))

    def build_service() -> RunService:
        store = build_store(settings)
        repository = RunRepository(create_session_factory(engine))
        secret_store = build_secret_store(settings)
        checker = TargetChecker(
            bench_binary=settings.bench_binary,
            run_secrets_dir=settings.secret_store.run_secrets_dir,
            store=secret_store,
            mounted_dir=settings.secret_store.mounted_dir,
            checked_from=checked_from(settings, os.environ),
        )
        return RunService(
            repository=repository,
            runner=build_runner(settings, secret_store),
            store=store,
            run_token_ttl_seconds=settings.run_token_ttl_seconds,
            runner_poll_seconds=settings.runner_poll_seconds,
            planner=Planner(settings.bench_binary, store),
            comparer=Comparer(settings.bench_binary, store),
            validator=SpecValidator(settings.bench_binary),
            targets=TargetService(repository, secret_store, checker, default_prometheus(settings)),
            managed_targets=ManagedTargetService(
                repository,
                settings.tuning_namespaces,
                build_deployer(settings, secret_store, checker),
                host_deployer=build_host_deployer(settings, secret_store, checker),
                secret_store=secret_store,
            ),
            proposer=Proposer(settings.bench_binary, store),
            default_prometheus=default_prometheus(settings),
            offline=settings.offline,
            image_registry=settings.image_registry,
        )

    return create_app(build_service, build_auth)


def build_deployer(
    settings: Settings, secret_store: SecretStore | None, checker: TargetChecker
) -> Deployer | None:
    """The deployer exists only where it can work: in Kubernetes (the kubernetes runner), with
    a bench binary for the flags, and with at least one tuning namespace granted."""
    if settings.kubernetes is None or settings.bench_binary is None:
        return None
    if not settings.tuning_namespaces:
        return None
    return Deployer(
        cluster=LightkubeManagedCluster(),
        render_knobs=bench_vllm_args(settings.bench_binary),
        checker=checker,
        store=secret_store,
        prometheus=default_prometheus(settings),
        image_pull_secrets=settings.kubernetes.image_pull_secrets,
        offline=settings.offline,
        monitor=(
            "scrape-annotations"
            if settings.tuning_monitor == "scrape-annotations"
            else "podmonitor"
        ),
    )


def build_host_deployer(
    settings: Settings, secret_store: SecretStore | None, checker: TargetChecker
) -> LauncherDeployer | None:
    """Host targets need no cluster: a bench binary for the flags and a secret store for the
    launcher's token ("Hosts outside Kubernetes")."""
    if settings.bench_binary is None or secret_store is None:
        return None
    return LauncherDeployer(
        render_knobs=bench_vllm_args(settings.bench_binary),
        checker=checker,
        store=secret_store,
        prometheus=default_prometheus(settings),
        offline=settings.offline,
    )


class ApiServer:
    """A uvicorn server plus the certificate reloader that belongs to its listener."""

    def __init__(
        self, app: FastAPI, settings: Settings, *, host: str = LISTEN_HOST, port: int | None = None
    ) -> None:
        config = uvicorn.Config(
            app,
            host=host,
            port=settings.port if port is None else port,
            **uvicorn_ssl_options(settings.tls),
        )
        config.load()  # builds the SSL context now, so the reloader can hold it
        self.server = uvicorn.Server(config)
        self._reloader: CertificateReloader | None = reloader_for(config.ssl, settings)

    def run(self) -> None:
        if self._reloader:
            self._reloader.start()
        try:
            self.server.run()
        finally:
            if self._reloader:
                self._reloader.stop()


def main(env: Mapping[str, str] | None = None, argv: Sequence[str] | None = None) -> None:
    """Serve the api, or with ``admin ...`` run a recovery command and exit."""
    logging.basicConfig(level=logging.INFO)
    arguments = list(sys.argv[1:] if argv is None else argv)
    try:
        settings = Settings.from_env(os.environ if env is None else env)
    except ConfigError as error:
        sys.exit(f"servtuner-api: {error}")
    if arguments[:1] == ["admin"]:
        engine = create_database_engine(build_database_url(settings.database, settings.tls))
        store = AuthStore(create_session_factory(engine))
        sys.exit(run_admin_command(arguments[1:], store))
    ApiServer(build_app(settings), settings).run()
