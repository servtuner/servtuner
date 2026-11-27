"""Local end to end, no cluster: Postgres (container) + servtuner-api + the real bench binary.

    PYTHONPATH=api uv run --project api python e2e/local.py mutual-tls|tls-off

Proves, on this machine, that one RunSpec posted to the api is launched through the
SubprocessRunner, run by bench (null engine), reported back through the events
endpoint and left as a complete run bundle, in each transport mode.
Everything it starts is torn down on exit, including on failure.

Python rather than bash: it needs HTTP polling with JSON assertions, mutual TLS
clients, and the api's own certificate and Postgres test support, which it imports.
"""

import json
import os
import socket
import subprocess
import sys
from contextlib import ExitStack
from pathlib import Path

import httpx2
from common import (
    REPOSITORY,
    Security,
    auth_environment,
    check,
    check_manifest_succeeded,
    client_context,
    create_first_admin,
    exercise_run,
    exercise_schedule,
    refuse_without_client_certificate,
    scratch_directory,
    stop_process,
    wait_for,
)
from testcontainers.community.postgres import PostgresContainer
from testcontainers.core.container import DockerContainer

from servtuner_api.datadir import run_bundle_dir
from tests.support.certs import LOOPBACK, CertificateAuthority, create_authority
from tests.support.postgres_tls import (
    DATABASE_NAME,
    DATABASE_USER,
    POSTGRES_IMAGE,
    POSTGRES_PORT,
    tls_postgres_container,
)


def free_port() -> int:
    # The api's URL (handed to bench) must be known before it listens, so port 0 is not usable.
    with socket.socket() as probe:
        probe.bind((LOOPBACK, 0))
        return int(probe.getsockname()[1])


def build_bench(output: Path) -> Path:
    binary = output / "bench"
    subprocess.run(
        ["go", "build", "-o", str(binary), "./cmd/servtuner"],
        cwd=REPOSITORY / "bench",
        check=True,
    )
    return binary


def start_postgres(
    stack: ExitStack,
    mode: Security,
    scratch: Path,
    authority: CertificateAuthority,
) -> dict[str, str]:
    """Database environment for the api; the container is stopped when ``stack`` closes."""
    if mode is Security.MUTUAL_TLS:
        mount = scratch / "pg-mount"
        mount.mkdir()
        container: DockerContainer = stack.enter_context(tls_postgres_container(authority, mount))
        return {
            "SERVTUNER_DATABASE_HOST": LOOPBACK,
            "SERVTUNER_DATABASE_PORT": str(container.get_exposed_port(POSTGRES_PORT)),
            "SERVTUNER_DATABASE_NAME": DATABASE_NAME,
            "SERVTUNER_DATABASE_USER": DATABASE_USER,
            "SERVTUNER_DATABASE_SSLMODE": "verify-full",
        }
    postgres = stack.enter_context(PostgresContainer(POSTGRES_IMAGE, driver="psycopg"))
    return {
        "SERVTUNER_DATABASE_HOST": LOOPBACK,
        "SERVTUNER_DATABASE_PORT": str(postgres.get_exposed_port(POSTGRES_PORT)),
        "SERVTUNER_DATABASE_NAME": postgres.dbname,
        "SERVTUNER_DATABASE_USER": postgres.username,
        "SERVTUNER_DATABASE_PASSWORD": postgres.password,
        "SERVTUNER_DATABASE_SSLMODE": "disable",
    }


def start_api(stack: ExitStack, environment: dict[str, str], log_file: Path) -> None:
    with log_file.open("wb") as log:
        api = subprocess.Popen(  # noqa: S603 - the console script of this project's own venv
            [str(Path(sys.executable).with_name("servtuner-api"))],
            env={**os.environ, **environment},
            stdout=log,
            stderr=subprocess.STDOUT,
        )

    stack.callback(stop_process, api)


def run_e2e(mode: Security) -> None:
    with scratch_directory() as scratch_name, ExitStack() as stack:
        scratch = Path(scratch_name)
        data_dir = scratch / "data"
        port = free_port()
        bench_binary = build_bench(scratch)
        authority = create_authority(scratch / "pki")
        environment = {
            "SERVTUNER_RUNNER": "subprocess",
            "SERVTUNER_ENGINE_OVERRIDE": "null",
            "SERVTUNER_BENCH_BINARY": str(bench_binary),
            "SERVTUNER_DATA_DIR": str(data_dir),
            "SERVTUNER_API_PORT": str(port),
            "SERVTUNER_TLS_ENABLED": mode.tls_flag,
        } | auth_environment(scratch, is_cookie_secure=mode is Security.MUTUAL_TLS)
        client_options: dict[str, object] = {"base_url": f"http://localhost:{port}"}
        if mode is Security.MUTUAL_TLS:
            # The api's own certificate is also bench's client certificate and its Postgres
            # client certificate (CN = database user): api/CLAUDE.md "SubprocessRunner".
            api_certificate = authority.issue(DATABASE_USER)
            client_certificate = authority.issue("e2e-client")
            environment |= {
                "SERVTUNER_TLS_CERT_FILE": str(api_certificate.cert_file),
                "SERVTUNER_TLS_KEY_FILE": str(api_certificate.key_file),
                "SERVTUNER_TLS_CLIENT_CA_FILE": str(authority.cert_file),
                "SSL_CERT_FILE": str(authority.cert_file),
            }
            context = client_context(
                authority.cert_file,
                (client_certificate.cert_file, client_certificate.key_file),
            )
            client_options = {
                "base_url": f"https://localhost:{port}",
                "verify": context,
            }
        environment |= start_postgres(stack, mode, scratch, authority)
        start_api(stack, environment, scratch / "api.log")

        try:
            with httpx2.Client(**client_options) as probe:  # type: ignore[arg-type]
                wait_for(
                    "the api to serve /healthz",
                    lambda: probe.get("/healthz").is_success,
                )
            if mode is Security.MUTUAL_TLS:
                refuse_without_client_certificate(f"https://localhost:{port}", authority.cert_file)
            with httpx2.Client(**client_options) as client:  # type: ignore[arg-type]
                create_first_admin(client)
                with httpx2.Client(**client_options) as stranger:  # type: ignore[arg-type]
                    refused = stranger.get("/runs")
                    check(
                        refused.status_code == 401,
                        f"a client with no session is refused ({refused.status_code})",
                    )
                # bench posts its events with the run token minted at launch.
                exercise_run(client, mode, lambda run_id: check_local_bundle(data_dir, run_id))
                exercise_schedule(client, mode)
        except Exception:
            print(
                f"--- api log ---\n{(scratch / 'api.log').read_text()[-4000:]}",
                flush=True,
            )
            raise


def check_local_bundle(data_dir: Path, run_id: str) -> None:
    check_manifest_succeeded(
        json.loads((run_bundle_dir(data_dir, run_id) / "manifest.json").read_text())
    )


if __name__ == "__main__":
    run_e2e(Security(sys.argv[1]))
    print("e2e-local passed", flush=True)
