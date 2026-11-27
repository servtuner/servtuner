"""The frontend's Playwright smoke test against the real api.

    PYTHONPATH=api uv run --project api python e2e/smoke.py      (or: just e2e-smoke)

Starts Postgres (container), servtuner-api with the SubprocessRunner and bench's
null engine in platform mode, and the Vite dev server proxying /api to it,
then runs `npx playwright test` in frontend/. Everything is torn down on exit.
One transport mode is enough here: the browser never talks to the api
directly (nginx or the dev proxy does), and both modes are covered by
e2e/local.py.
"""

import os
import signal
import subprocess
import sys
from contextlib import ExitStack
from pathlib import Path

import httpx2
from common import (
    E2E_ADMIN_PASSWORD,
    E2E_ADMIN_USERNAME,
    REPOSITORY,
    auth_environment,
    create_first_admin,
    scratch_directory,
    stop_process,
    wait_for,
)
from local import build_bench, free_port, start_api, start_postgres

from servtuner_api.settings.models import TransportMode
from tests.support.certs import LOOPBACK, create_authority

FRONTEND = REPOSITORY / "frontend"
SMOKE_TUNING_NAMESPACE = "servtuner-smoke"  # the smoke.pw.ts managed target's namespace


def start_frontend(stack: ExitStack, api_url: str, port: int, log_file: Path) -> None:
    with log_file.open("wb") as log:
        vite = subprocess.Popen(  # noqa: S603 - this repository's own dev server
            [
                "npx",
                "vite",
                "--host",
                LOOPBACK,
                "--port",
                str(port),
                "--strictPort",
            ],  # noqa: S607
            cwd=FRONTEND,
            env={**os.environ, "SERVTUNER_DEV_API": api_url},
            stdout=log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    stack.callback(stop_group, vite)


def stop_group(process: subprocess.Popen[bytes]) -> None:
    """npx runs vite as its child, so stopping npx alone leaves the server running:
    signal the whole process group (its own session), then reap npx."""
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    stop_process(process)


def main() -> int:
    with scratch_directory() as scratch_name, ExitStack() as stack:
        scratch = Path(scratch_name)
        api_port, frontend_port = free_port(), free_port()
        environment = {
            "SERVTUNER_RUNNER": "subprocess",
            "SERVTUNER_ENGINE_OVERRIDE": "null",
            "SERVTUNER_BENCH_BINARY": str(build_bench(scratch)),
            "SERVTUNER_DATA_DIR": str(scratch / "data"),
            "SERVTUNER_API_PORT": str(api_port),
            "SERVTUNER_TLS_MODE": TransportMode.PLATFORM.value,
            # Lets the smoke store a managed target; nothing is deployed (no Kubernetes runner).
            "SERVTUNER_TUNING_NAMESPACES": SMOKE_TUNING_NAMESPACE,
        } | auth_environment(scratch, is_cookie_secure=False)
        authority = create_authority(scratch / "pki")
        environment |= start_postgres(stack, TransportMode.PLATFORM, scratch, authority)
        start_api(stack, environment, scratch / "api.log")
        api_url = f"http://{LOOPBACK}:{api_port}"
        frontend_url = f"http://{LOOPBACK}:{frontend_port}/"
        start_frontend(stack, api_url, frontend_port, scratch / "vite.log")
        with httpx2.Client() as probe:
            healthz = f"{api_url}/healthz"
            wait_for("the api to serve /healthz", lambda: probe.get(healthz).is_success)
            wait_for(
                "the dev server to proxy the api",
                lambda: probe.get(f"{frontend_url}api/healthz").is_success,
            )
        # The admin exists before the browser starts; every spec signs in through the login page.
        with httpx2.Client(base_url=api_url) as admin:
            create_first_admin(admin)
        result = subprocess.run(  # noqa: S603
            # SERVTUNER_SMOKE_SPEC runs another spec against the same stack (a screenshot, say).
            [
                "npx",
                "playwright",
                "test",
                os.environ.get("SERVTUNER_SMOKE_SPEC", "smoke.pw.ts"),
            ],  # noqa: S607
            cwd=FRONTEND,
            env={
                **os.environ,
                "SERVTUNER_SMOKE_URL": frontend_url,
                "SERVTUNER_SMOKE_USERNAME": E2E_ADMIN_USERNAME,
                "SERVTUNER_SMOKE_PASSWORD": E2E_ADMIN_PASSWORD,
            },
            check=False,
        )
        if result.returncode != 0:
            for name in ("api.log", "vite.log"):
                print(
                    f"--- {name} ---\n{(scratch / name).read_text()[-4000:]}",
                    flush=True,
                )
        return result.returncode


if __name__ == "__main__":
    sys.exit(main())
