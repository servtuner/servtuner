"""What the local and the cluster end to end share: checks, polling, and the run exercise.

Both scripts post one RunSpec through the api, wait for bench to finish it, and verify the same
observable result (in each transport mode). They differ only in where the api runs and
how the bundle on disk is read, so the exercise takes that as a callback.
"""

import json
import ssl
import subprocess
import tempfile
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx2

from servtuner_api.settings.models import TransportMode
from tests.support.specs import example_bytes

REPOSITORY = Path(__file__).resolve().parents[1]
TIMEOUT_SECONDS = 20.0
# The api's scheduler wakes every 30 s, then the run takes up to TIMEOUT_SECONDS.
SCHEDULE_TIMEOUT_SECONDS = 30.0 + TIMEOUT_SECONDS
POLL_SECONDS = 0.5
SCRATCH_PREFIX = "servtuner-e2e-"
STOP_TIMEOUT_SECONDS = 10.0
TERMINAL_STATES = {"succeeded", "failed", "cancelled"}
# The seven steps of bench's run pipeline, read from the schema so they are defined once.
EVENTS_SCHEMA = REPOSITORY / "schema" / "json" / "events.schema.json"
PIPELINE_STEPS = json.loads(EVENTS_SCHEMA.read_text())["definitions"]["pipelineStep"]["enum"]
# An example whose target TLS mode matches the hop under test; the null engine never connects.
EXAMPLE_FOR_MODE = {
    TransportMode.MTLS: "minimal-vllm-client-cert",
    TransportMode.PLATFORM: "router-bifrost-tls-off",
}


# User auth: local runs make the first admin with a setup token file and sign in.
# Neither value guards anything outside the throwaway api of one e2e run.
E2E_SETUP_TOKEN = "e2e-setup-token-for-a-throwaway-api"
E2E_ADMIN_USERNAME = "e2e-admin"
E2E_ADMIN_PASSWORD = "e2e admin passphrase"


def auth_environment(scratch: Path, is_cookie_secure: bool) -> dict[str, str]:
    """The api's auth settings for a local e2e. httpx sends a Secure cookie only over https, so a
    plain-HTTP api turns Secure off (the chart's cookie.secure: false)."""
    token_file = scratch / "setup-token"
    token_file.write_text(E2E_SETUP_TOKEN)
    return {
        "SERVTUNER_AUTH_MODE": "local",
        "SERVTUNER_AUTH_SETUP_TOKEN_FILE": str(token_file),
        "SERVTUNER_AUTH_COOKIE_SECURE": "true" if is_cookie_secure else "false",
    }


def create_first_admin(client: httpx2.Client) -> None:
    """Before setup every route asks for it; setup signs the client in as the first admin."""
    before = client.get("/runs")
    check(
        before.status_code == 401 and before.json()["detail"]["code"] == "setup_required",
        f"before setup the api asks for it ({before.status_code})",
    )
    created = client.post(
        "/auth/setup",
        json={"username": E2E_ADMIN_USERNAME, "password": E2E_ADMIN_PASSWORD},
        headers={"X-Servtuner-Setup-Token": E2E_SETUP_TOKEN},
    )
    check(
        created.status_code == 201,
        f"POST /auth/setup made the first admin ({created.status_code})",
    )
    check(
        client.get("/auth/me").json()["role"] == "admin",
        "the client is signed in as admin",
    )


def scratch_directory() -> tempfile.TemporaryDirectory[str]:
    return tempfile.TemporaryDirectory(prefix=SCRATCH_PREFIX)


def stop_process(
    process: subprocess.Popen[bytes], timeout_seconds: float = STOP_TIMEOUT_SECONDS
) -> None:
    """Terminate, wait, then kill: a child the script started must never outlive it."""
    process.terminate()
    try:
        process.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait()


def check(condition: bool, description: str) -> None:
    if not condition:
        raise AssertionError(description)
    print(f"ok   {description}", flush=True)


def wait_for(
    description: str, probe: Callable[[], Any], timeout_seconds: float = TIMEOUT_SECONDS
) -> Any:
    deadline = time.monotonic() + timeout_seconds
    last_error: Exception | None = None
    while True:
        try:
            result = probe()
        except httpx2.TransportError as error:
            result, last_error = None, error
        if result:
            return result
        if time.monotonic() > deadline:
            raise TimeoutError(f"timed out waiting for {description}; last error: {last_error!r}")
        time.sleep(POLL_SECONDS)


def client_context(
    authority_certificate: Path, client_certificate: tuple[Path, Path] | None = None
) -> ssl.SSLContext:
    """Trust the private CA; present ``(cert_file, key_file)`` if given, else no identity."""
    context = ssl.create_default_context(cafile=str(authority_certificate))
    if client_certificate is not None:
        context.load_cert_chain(*(str(path) for path in client_certificate))
    return context


def refuse_without_client_certificate(base_url: str, authority_certificate: Path) -> None:
    try:
        with httpx2.Client(
            base_url=base_url, verify=client_context(authority_certificate)
        ) as client:
            client.get("/healthz")
    except httpx2.TransportError:
        check(True, "mtls: a request without a client certificate is refused")
    else:
        raise AssertionError("mtls: a request without a client certificate was served")


def check_manifest_succeeded(manifest: dict[str, Any]) -> None:
    check(manifest["status"] == "succeeded", "the bundle manifest.json says succeeded")


def exercise_run(
    client: httpx2.Client,
    mode: TransportMode,
    check_bundle: Callable[[str], None],
    document: bytes | None = None,
    timeout_seconds: float = TIMEOUT_SECONDS,
) -> str:
    """Create a run, wait for it, check its events, bundle and listing; returns the run ID.

    ``document`` is the RunSpec to submit; by default the mode's schema example, which the null
    engine runs in seconds. A real load run needs a ``timeout_seconds`` that covers its stages.
    """
    created = client.post(
        "/runs",
        content=document or example_bytes("runspec", "valid", EXAMPLE_FOR_MODE[mode]),
        headers={"Content-Type": "application/json"},
    )
    check(created.status_code == 201, f"POST /runs accepted ({created.status_code})")
    run_id: str = created.json()["id"]

    def terminal_run() -> dict[str, object] | None:
        run = client.get(f"/runs/{run_id}").json()
        return run if run["status"] in TERMINAL_STATES else None

    run = wait_for(f"run {run_id} to finish", terminal_run, timeout_seconds)
    check(run["status"] == "succeeded", f"run {run_id} succeeded (was {run['status']})")

    events = client.get(f"/runs/{run_id}/events").json()
    steps = list(dict.fromkeys(event["step"] for event in events))
    check(
        steps == PIPELINE_STEPS,
        f"the events timeline has bench's seven steps ({steps})",
    )

    check_bundle(run_id)
    listed = [entry["id"] for entry in client.get("/runs").json()]
    check(run_id in listed, "GET /runs lists the run")
    return run_id


def exercise_schedule(
    client: httpx2.Client,
    mode: TransportMode,
    timeout_seconds: float = SCHEDULE_TIMEOUT_SECONDS,
) -> str:
    """Create a schedule due now and wait for the api's scheduler to launch and finish its run.

    The scheduler wakes every 30 s, so this proves the loop itself, not only the routes.
    """
    spec = json.loads(example_bytes("runspec", "valid", EXAMPLE_FOR_MODE[mode]))
    created = client.post("/schedules", json={"name": "e2e", "spec": spec, "every_hours": 24})
    check(created.status_code == 201, f"POST /schedules accepted ({created.status_code})")
    schedule_id: str = created.json()["id"]

    def finished_run() -> dict[str, Any] | None:
        runs = client.get(f"/schedules/{schedule_id}").json()["runs"]
        return runs[0] if runs and runs[0]["status"] in TERMINAL_STATES else None

    description = f"schedule {schedule_id} to launch and finish a run"
    run = wait_for(description, finished_run, timeout_seconds)
    check(
        run["status"] == "succeeded",
        f"the scheduled run succeeded (was {run['status']})",
    )
    labels = client.get(f"/runs/{run['run_id']}").json()["labels"]
    check(
        labels.get("servtuner.schedule") == schedule_id,
        "the scheduled run carries its label",
    )
    return schedule_id
