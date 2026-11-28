"""Local end to end, no cluster: Postgres (container) + servtuner-api + the real bench binary.

    PYTHONPATH=api uv run --project api python e2e/local.py mutual-tls|tls-off

Proves, on this machine, that one RunSpec posted to the api is launched through the
SubprocessRunner, run by bench, reported back through the events endpoint and left as a
complete run bundle, in each transport mode. mutual-tls runs bench's null engine (no requests);
tls-off is the happy path: the real loadgen engine sends a handful of requests (about 15 in all,
at 1 RPS) to the repository's mock on localhost. It is a smoke test, never a load test: it checks
that requests flow end to end, not what capacity they measure.
Everything it starts is torn down on exit, including on failure.

Python rather than bash: it needs HTTP polling with JSON assertions, mutual TLS
clients, and the api's own certificate and Postgres test support, which it imports.
"""

import hashlib
import json
import os
import socket
import subprocess
import sys
from contextlib import ExitStack
from pathlib import Path
from typing import Any

import httpx2
from common import (
    REPOSITORY,
    Security,
    auth_environment,
    check,
    check_manifest_succeeded,
    check_version,
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
from servtuner_api.version import DEV_VERSION, UNKNOWN_COMMIT
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


# The happy path's mock and load. The mock answers as fast as the bench e2e's (bench/internal/e2e);
# one 5 s stage at 1 RPS after a 2 s warmup is about 7 requests per run: enough to prove the path,
# far too few to measure anything. The stage-length warnings this triggers are expected.
MOCK_MODEL = "mock-model"
MOCK_TTFT = "20ms"  # time to first token the mock simulates
MOCK_ITL = "2ms"  # gap between the mock's tokens
SMOKE_WARMUP = "2s"
SMOKE_STAGE = {"rate": 1, "duration": "5s"}
# Building bench, warmup, the stage, then analysis and finalizing, with room for a slow runner.
SMOKE_RUN_TIMEOUT_SECONDS = 60.0
SCHEDULER_WAKE_SECONDS = 30.0  # the api's scheduler loop interval, waited out before the run starts


def start_mock(stack: ExitStack, scratch: Path) -> str:
    """Build and start the repository's OpenAI mock on loopback; returns its /v1 endpoint."""
    binary = scratch / "openai-mock"
    subprocess.run(
        ["go", "build", "-o", str(binary), "."],
        cwd=REPOSITORY / "deploy" / "mocks" / "openai-mock",
        check=True,
    )
    port = free_port()
    address = f"{LOOPBACK}:{port}"
    with (scratch / "mock.log").open("wb") as log:
        mock = subprocess.Popen(  # noqa: S603 - the mock this script just built
            [str(binary), "--listen", address, "--tls", "false", "--model", MOCK_MODEL]
            + ["--ttft", MOCK_TTFT, "--itl", MOCK_ITL],
            stdout=log,
            stderr=subprocess.STDOUT,
        )
    stack.callback(stop_process, mock)

    def is_listening() -> bool:
        with socket.socket() as probe:
            return probe.connect_ex((LOOPBACK, port)) == 0

    wait_for("the mock to listen", is_listening)
    return f"http://{address}/v1"


def smoke_runspec(endpoint: str) -> dict[str, Any]:
    """The happy path's RunSpec: the real loadgen engine against the mock, a few requests."""
    return {
        "apiVersion": "servtuner.dev/v2",
        "kind": "RunSpec",
        "name": "e2e-happy-path",
        "target": {
            "type": "openai-compatible",
            "endpoint": endpoint,
            "model": MOCK_MODEL,
            "tls": {"enabled": False},
        },
        "engine": {"type": "loadgen"},
        "workload": {
            "streaming": True,
            "dataset": {
                "type": "synthetic",
                "input_tokens": {"mean": 32},
                "output_tokens": {"mean": 8},
            },
        },
        "load": {"warmup": SMOKE_WARMUP, "stages": [SMOKE_STAGE]},
    }


def check_happy_path(client: httpx2.Client, data_dir: Path, run_id: str) -> None:
    """Requests reached the mock and came back, and the bundle and the api agree on it."""
    bundle = run_bundle_dir(data_dir, run_id)
    manifest = json.loads((bundle / "manifest.json").read_text())
    for entry in manifest["files"]:
        data = (bundle / entry["path"]).read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        check(
            len(data) == entry["size_bytes"] and digest == entry["sha256"],
            f"{entry['path']} matches the manifest",
        )
    listed = {entry["path"] for entry in manifest["files"]}
    for path in ("requests.parquet", "summary.json", "analysis.json"):
        check(path in listed, f"the bundle has {path}")
    stage = json.loads((bundle / "summary.json").read_text())["stages"][0]
    check(stage["requests"] > 0, f"the stage sent requests ({stage['requests']})")
    check(stage["errors"] == 0, f"every request was answered ({stage['errors']} errors)")
    analysis = client.get(f"/runs/{run_id}/analysis")
    check(analysis.status_code == 200, f"the api serves the analysis ({analysis.status_code})")


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
            "SERVTUNER_DATABASE_AUTH": "certificate",
        }
    postgres = stack.enter_context(PostgresContainer(POSTGRES_IMAGE, driver="psycopg"))
    return {
        "SERVTUNER_DATABASE_HOST": LOOPBACK,
        "SERVTUNER_DATABASE_PORT": str(postgres.get_exposed_port(POSTGRES_PORT)),
        "SERVTUNER_DATABASE_NAME": postgres.dbname,
        "SERVTUNER_DATABASE_USER": postgres.username,
        "SERVTUNER_DATABASE_AUTH": "password",
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
            "SERVTUNER_BENCH_BINARY": str(bench_binary),
            "SERVTUNER_DATA_DIR": str(data_dir),
            "SERVTUNER_API_PORT": str(port),
            "SERVTUNER_TLS_ENABLED": mode.tls_flag,
        } | auth_environment(scratch, is_cookie_secure=mode is Security.MUTUAL_TLS)
        client_options: dict[str, object] = {"base_url": f"http://localhost:{port}"}
        smoke_spec: dict[str, Any] | None = None
        if mode is Security.MUTUAL_TLS:
            environment["SERVTUNER_ENGINE_OVERRIDE"] = "null"
        else:
            smoke_spec = smoke_runspec(start_mock(stack, scratch))
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
                # The api runs from source here, so no release stamped it.
                check_version(client, DEV_VERSION, UNKNOWN_COMMIT)
                with httpx2.Client(**client_options) as stranger:  # type: ignore[arg-type]
                    refused = stranger.get("/runs")
                    check(
                        refused.status_code == 401,
                        f"a client with no session is refused ({refused.status_code})",
                    )
                # bench posts its events with the run token minted at launch.
                if smoke_spec is None:
                    exercise_run(client, mode, lambda run_id: check_local_bundle(data_dir, run_id))
                    exercise_schedule(client, mode)
                else:

                    def check_bundle(run_id: str) -> None:
                        check_local_bundle(data_dir, run_id)
                        check_happy_path(client, data_dir, run_id)

                    exercise_run(
                        client,
                        mode,
                        check_bundle,
                        document=json.dumps(smoke_spec).encode(),
                        timeout_seconds=SMOKE_RUN_TIMEOUT_SECONDS,
                    )
                    exercise_schedule(
                        client,
                        mode,
                        SCHEDULER_WAKE_SECONDS + SMOKE_RUN_TIMEOUT_SECONDS,
                        spec=smoke_spec,
                    )
        except Exception:
            print(
                f"--- api log ---\n{(scratch / 'api.log').read_text()[-4000:]}",
                flush=True,
            )
            if (scratch / "mock.log").exists():
                print(f"--- mock log ---\n{(scratch / 'mock.log').read_text()[-2000:]}", flush=True)
            raise


def check_local_bundle(data_dir: Path, run_id: str) -> None:
    check_manifest_succeeded(
        json.loads((run_bundle_dir(data_dir, run_id) / "manifest.json").read_text())
    )


if __name__ == "__main__":
    run_e2e(Security(sys.argv[1]))
    print("e2e-local passed", flush=True)
