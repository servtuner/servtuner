"""The tuning loop on a cluster: a session tunes the mock as a managed vLLM.

Used by ``e2e/cluster.py --tuning``. The mock answers as vLLM (deploy/mocks/README.md, "As
vLLM"): it boots after a delay, fails to boot with ``--tensor-parallel-size=4`` (the KV-cache
message), caps concurrency at ``--max-num-seqs``, and reports ``cache_config_info``. The session
must:
- record the tp=4 boot as infeasible, with the deployer's rule (vllm-serving.md section 4);
- run configurations as Jobs whose runs succeed;
- end with a recommended command line and the target scaled to zero;
and deleting the managed target must remove what servtuner deployed.
"""

import json
import os
import shutil
import subprocess
import tempfile
import time
from collections.abc import Callable
from pathlib import Path
from typing import Any

import httpx2
from common import REPOSITORY

TARGET_NAME = "vllm-mock"
# The mock as vLLM: 2 ms per token, so chat-short's 256 output tokens take about 0.6 s and
# max_num_seqs=8 holds the mock near 13 RPS: the probe ladder's last stage (16 RPS) overloads it.
# Every stage sends at least 240 requests in 60 s: a p95 needs 200.
TARGET_ENV = {
    "VLLM_MOCK_BOOT_SECONDS": "5",
    "VLLM_MOCK_INFEASIBLE": "--tensor-parallel-size=4",
    "OPENAI_MOCK_TTFT": "100ms",
    "OPENAI_MOCK_ITL": "2ms",
}
SPACE: dict[str, Any] = {
    "objective": "slo_max_rps",  # the mock has no GPUs, so there is no per-GPU objective
    "min_concurrency": 1,
    "knobs": [
        {"name": "vllm.tensor_parallel_size", "tier": "feasibility", "values": [1, 4]},
        {
            "name": "vllm.max_num_seqs",
            "tier": "scheduler",
            "min": 4,
            "max": 64,
            "default": 8,
        },
    ],
    "ladder": {
        "probe_rps": [4, 8, 16],
        "screen_factors": [0.7, 1, 1.3],
        "full_factors": [0.7, 1, 1.3],
    },
    "confirm": {"top": 2, "repeats": 2},
}
# Four runs of about four minutes, two boot probes and two deploys, with margin.
SESSION_TIMEOUT_SECONDS = 50 * 60
POLL_SECONDS = 15
FINAL = ("done", "failed", "cancelled")


class TuningCheckError(AssertionError):
    pass


def check(condition: bool, message: str) -> None:
    if not condition:
        raise TuningCheckError(message)


def exercise_tuning(
    api: httpx2.Client,
    *,
    image: str,
    target_namespace: str,
    kubectl_output: Callable[..., str],
) -> str:
    """Run one session end to end; returns a run id of the session for the UI check."""
    created = api.post(
        "/managed-targets",
        json={
            "name": TARGET_NAME,
            "namespace": target_namespace,
            "spec": {
                "image": image,
                "model": "mock/vllm",
                "gpus": 0,
                "cpu": "500m",
                "memory": "256Mi",
                "env": TARGET_ENV,
                "model_cache": {"size": "64Mi"},
            },
        },
    )
    check(
        created.status_code == 201,
        f"managed target not created: {created.status_code} {created.text}",
    )
    target = created.json()
    started = api.post(
        "/tuning-sessions",
        json={
            "managed_target_id": target["id"],
            "preset": "chat-short@1",
            "slos": {"ttft_p95": "2s", "e2e_p95": "30s"},
            "space": SPACE,
            "budget": {"max_runs": 4, "max_hours": 1},
            "stage_duration": "60s",
            "warmup": "30s",
        },
    )
    check(
        started.status_code == 201,
        f"session not started: {started.status_code} {started.text}",
    )
    session = wait_for_session(api, started.json()["id"])

    outcome = session.get("outcome") or {}
    check(session["status"] == "done", f"session ended {session['status']}: {outcome}")
    infeasible = [
        entry["boot_result"]
        for entry in session["history"]
        if entry.get("boot_result", {}).get("outcome") == "infeasible"
    ]
    check(
        any(result["knobs"].get("vllm.tensor_parallel_size") == 4 for result in infeasible),
        f"the tp=4 boot is not recorded as infeasible: {session['history']}",
    )
    check(
        all(result.get("reason", "").startswith("no-kv-blocks") for result in infeasible),
        f"an infeasible boot has another rule than no-kv-blocks: {infeasible}",
    )
    run_ids = [run_id for entry in session["history"] for run_id in entry.get("run_ids", [])]
    check(bool(run_ids), f"the session ran nothing: {session['history']}")
    statuses = {run_id: api.get(f"/runs/{run_id}").json()["status"] for run_id in run_ids}
    check("succeeded" in statuses.values(), f"no run of the session succeeded: {statuses}")
    recommendation = outcome.get("recommendation") or {}
    check(
        "--tensor-parallel-size=1" in recommendation.get("command", ""),
        f"the recommendation does not use the configuration that boots: {outcome}",
    )
    print(
        f"     recommended: {recommendation['command']} ({outcome.get('outcome')})",
        flush=True,
    )

    replicas = kubectl_output(
        "get",
        "deployment",
        TARGET_NAME,
        "-n",
        target_namespace,
        "-o",
        "jsonpath={.spec.replicas}",
    )
    check(
        replicas.strip() == "0",
        f"the managed vLLM was not scaled to zero: replicas {replicas!r}",
    )
    deleted = api.delete(f"/managed-targets/{target['id']}")
    check(
        deleted.status_code == 204,
        f"managed target not deleted: {deleted.status_code} {deleted.text}",
    )
    left = kubectl_output(
        "get",
        "deployment,service",
        "-n",
        target_namespace,
        "-l",
        f"servtuner.dev/managed-target={TARGET_NAME}",
        "-o",
        "name",
    )
    check(left.strip() == "", f"teardown left objects behind: {left}")
    return run_ids[-1]


# --- the deploy path from the UI (--ui): frontend/e2e/managed.pw.ts drives the browser

UI_SPEC = "managed.pw.ts"
# Generous SLOs, as the api-driven sessions use: the question is the target's capacity.
UI_SLOS = {"TTFT p95": "2s", "E2E p95": "30s"}


# The UI session's space: the scheduler knob only, probing well below what the mock serves with
# TARGET_ENV (about 0.6 s per chat-short request, so max_num_seqs=16 already serves about 25 RPS).
# It also holds at the mock's defaults (about 5.3 s per request: 64 seqs serve about 12 RPS).
UI_MOCK_SPACE: dict[str, Any] = {
    "objective": "slo_max_rps",
    "min_concurrency": 1,
    "knobs": [
        {
            "name": "vllm.max_num_seqs",
            "tier": "scheduler",
            "min": 16,
            "max": 256,
            "default": 64,
        }
    ],
    "ladder": {
        "probe_rps": [2, 4, 8],
        "screen_factors": [0.7, 1, 1.3],
        "full_factors": [0.7, 1, 1.3],
    },
}


def kubernetes_ui_target(*, image: str, target_namespace: str) -> dict[str, Any]:
    """The mock as a managed vLLM, typed into the form (managed.pw.ts's UiTarget), with the
    api-driven session's TARGET_ENV in its Environment field."""
    return {
        "name": "vllm-ui",
        "placement": "kubernetes",
        "fields": {
            "Namespace": target_namespace,
            "Image": image,
            "Model": "mock/vllm",
            "GPUs": "0",
            "CPU": "500m",
            "Memory": "256Mi",
            "Model cache size": "64Mi",
            "Environment": "\n".join(f"{name}={value}" for name, value in TARGET_ENV.items()),
            # Harmless on kind: every node is linux, and no node has this taint.
            "Node selector": "kubernetes.io/os=linux",
            "Tolerations": "- {key: servtuner.dev/e2e, operator: Exists, effect: NoSchedule}",
        },
        "knobs": ["vllm.max_num_seqs: 32", "vllm.max_num_seqs: 64"],
        "redeployedArg": "--max-num-seqs=64",
        "logText": "servtuner mock",
        "space": json.dumps(UI_MOCK_SPACE),
        "slos": UI_SLOS,
    }


def host_ui_target(
    *,
    launcher_url: str,
    endpoint: str,
    launcher_token_secret: str,
    vllm_key_secret: str,
) -> dict[str, Any]:
    """The GPU host's vLLM through its launcher, typed into the form (managed.pw.ts's UiTarget)."""
    return {
        "name": "rtx3070-ui",
        "placement": "host",
        "fields": {
            "Metrics namespace label": HOST_NAMESPACE,
            "Model": HOST_MODEL,
            "Served model name": "tiny",
            "Launcher URL": launcher_url,
            "Launcher token Secret": launcher_token_secret,
            "vLLM endpoint": endpoint,
            "vLLM API key Secret": vllm_key_secret,
            "Metrics job": HOST_METRICS["job"],
            "Metrics pod label": HOST_METRICS["pod"],
            "GPU exporter host label": HOST_METRICS["node"],
            "Base flags": "\n".join(HOST_BASE_FLAGS),
        },
        "knobs": [
            "vllm.gpu_memory_utilization: 0.75\nvllm.max_num_seqs: 32",
            "vllm.gpu_memory_utilization: 0.75\nvllm.max_num_seqs: 64",
        ],
        "redeployedArg": "--max-num-seqs=64",
        "logText": "Application startup complete",  # uvicorn's line once vLLM serves
        "space": json.dumps(
            {
                **HOST_SPACE,
                "knobs": [knob for knob in HOST_SPACE["knobs"] if knob["tier"] == "scheduler"],
            }
        ),
        "slos": UI_SLOS,
    }


def exercise_managed_ui(
    api: httpx2.Client,
    *,
    ui_url: str,
    target: dict[str, Any],
    target_namespace: str | None,
    kubectl_output: Callable[..., str],
) -> str:
    """Playwright adds, deploys, redeploys, benchmarks, frees, tunes and deletes the target in
    the UI; then nothing of it may be left. Returns the newest run's id for the UI check.
    """
    # Its own output folder: the smoke run clears frontend/test-results when it starts, which
    # broke a cluster run writing its traces there. Kept on failure for the trace.
    output = Path(tempfile.mkdtemp(prefix="servtuner-managed-ui-"))
    result = subprocess.run(  # noqa: S603
        ["npx", "playwright", "test", UI_SPEC, "--output", str(output)],  # noqa: S607
        cwd=REPOSITORY / "frontend",
        env={
            **os.environ,
            "SERVTUNER_SMOKE_URL": ui_url,
            "SERVTUNER_UI_TARGET": json.dumps(target),
        },
        check=False,
    )
    check(
        result.returncode == 0,
        f"the managed vLLM UI flow failed (exit {result.returncode}); traces in {output}",
    )
    shutil.rmtree(output, ignore_errors=True)
    if target_namespace is not None:
        left = kubectl_output(
            "get",
            "deployment,service,podmonitor",
            "-n",
            target_namespace,
            "-l",
            f"servtuner.dev/managed-target={target['name']}",
            "-o",
            "name",
        )
        check(left.strip() == "", f"Delete in the UI left objects behind: {left}")
    check(api.get("/managed-targets").json() == [], "the managed target is still listed")
    runs = api.get("/runs").json()
    check(bool(runs), "the UI flow created no run")
    return str(runs[0]["id"])


def wait_for_session(api: httpx2.Client, session_id: str) -> dict[str, Any]:
    """Poll until the session ends, printing each new step."""
    deadline = time.monotonic() + SESSION_TIMEOUT_SECONDS
    last = ""
    while True:
        session: dict[str, Any] = api.get(f"/tuning-sessions/{session_id}").json()
        step = session.get("step") or {}
        proposed = step.get("proposed") or {}
        line = " ".join(
            str(part)
            for part in (
                session["status"],
                step.get("phase", "-"),
                proposed.get("config_id", ""),
            )
        )
        line += f" {proposed.get('knobs', '')}"
        if step.get("waiting"):
            line += f" ({step['waiting']})"
        if line != last:
            print(f"     session: {line} runs={session['runs_started']}", flush=True)
            last = line
        if session["status"] in FINAL:
            return session
        if time.monotonic() > deadline:
            raise TimeoutError(
                f"the session did not end in {SESSION_TIMEOUT_SECONDS // 60} min: {line}"
            )
        time.sleep(POLL_SECONDS)


# --- a real GPU host through servtuner launcher: `e2e/cluster.py --tuning-host`

# The RTX 3070 host's model. Real hardware decides whether a
# configuration boots: vLLM 0.34 started 0.95 on this card next to the desktop and sized its
# extensible KV cache the same as at 0.75 (vllm-serving.md section 6), so the checks accept either
# outcome and require only that every boot was recorded with evidence. When both boot with the
# same blocks they are one configuration: 0.75 is kept, and the outcome says so.
HOST_BASE_FLAGS = [
    "--max-model-len=4096",
    "--enable-auto-tool-choice",
    "--tool-call-parser=hermes",
]
HOST_MODEL = "Qwen/Qwen2.5-0.5B-Instruct"
HOST_NAMESPACE = "servtuner-external"  # the label the servtuner-external job gives its series
HOST_METRICS = {"job": "servtuner-external", "pod": "vllm-lan", "node": "vllm-lan-gpu"}
HOST_SPACE: dict[str, Any] = {
    "objective": "slo_max_rps",
    "min_concurrency": 1,
    "knobs": [
        {
            "name": "vllm.gpu_memory_utilization",
            "tier": "feasibility",
            "values": [0.75, 0.95],
        },
        {
            "name": "vllm.max_num_seqs",
            "tier": "scheduler",
            "min": 16,
            "max": 256,
            "default": 32,
        },
    ],
    # 300 to 1200 requests per 60 s stage: enough for every p95.
    "ladder": {
        "probe_rps": [5, 10, 20],
        "screen_factors": [0.7, 1, 1.3],
        "full_factors": [0.7, 1, 1.3],
    },
    "confirm": {"top": 2, "repeats": 2},
}
HOST_INFEASIBLE_RULES = ("not-enough-free-memory", "cuda-oom", "no-kv-blocks")


def exercise_tuning_host(
    api: httpx2.Client,
    *,
    launcher_url: str,
    endpoint: str,
    launcher_token_secret: str,
    vllm_key_secret: str,
) -> str:
    """Tune the host's vLLM; the launcher and both Secrets already exist. Returns a run id."""
    created = api.post(
        "/managed-targets",
        json={
            "name": "rtx3070",
            "namespace": HOST_NAMESPACE,
            "spec": {
                "placement": "host",
                "launcher_url": launcher_url,
                "launcher_token": {"name": launcher_token_secret, "key": "token"},
                "endpoint": endpoint,
                "model": HOST_MODEL,
                "served_model_name": "tiny",
                "base_flags": HOST_BASE_FLAGS,
                "auth": {"name": vllm_key_secret, "key": "token"},
                "metrics": HOST_METRICS,
            },
        },
    )
    check(
        created.status_code == 201,
        f"host target not created: {created.status_code} {created.text}",
    )
    target = created.json()
    started = api.post(
        "/tuning-sessions",
        json={
            "managed_target_id": target["id"],
            "preset": "chat-short@1",
            "slos": {"ttft_p95": "500ms", "itl_p95": "50ms"},
            "space": HOST_SPACE,
            "budget": {"max_runs": 4, "max_hours": 2},
            "stage_duration": "60s",
            "warmup": "30s",
        },
    )
    check(
        started.status_code == 201,
        f"session not started: {started.status_code} {started.text}",
    )
    session = wait_for_session(api, started.json()["id"])
    outcome = session.get("outcome") or {}
    check(session["status"] == "done", f"session ended {session['status']}: {outcome}")
    boots = [entry["boot_result"] for entry in session["history"] if "boot_result" in entry]
    check(
        {b["knobs"].get("vllm.gpu_memory_utilization") for b in boots} == {0.75, 0.95},
        f"not every feasibility value was boot-probed: {boots}",
    )
    for boot in boots:
        if boot["outcome"] == "feasible":
            check(
                boot.get("num_gpu_blocks", 0) > 0,
                f"a feasible boot has no KV blocks: {boot}",
            )
            check(
                "kv_sizing" in boot,
                f"a feasible boot on vLLM 0.34 has no kv_sizing: {boot}",
            )
        else:
            check(
                boot.get("reason", "").startswith(HOST_INFEASIBLE_RULES),
                f"an infeasible boot has an unexpected rule: {boot}",
            )
    run_ids = [run_id for entry in session["history"] for run_id in entry.get("run_ids", [])]
    statuses = {run_id: api.get(f"/runs/{run_id}").json()["status"] for run_id in run_ids}
    check("succeeded" in statuses.values(), f"no run of the session succeeded: {statuses}")
    recommendation = outcome.get("recommendation") or {}
    check(
        "--gpu-memory-utilization=" in recommendation.get("command", ""),
        f"the recommendation does not name the tuned utilization: {outcome}",
    )
    feasible = [b for b in boots if b["outcome"] == "feasible"]
    if len(feasible) == len(boots) == 2 and len({b.get("num_gpu_blocks") for b in feasible}) == 1:
        check(
            "--gpu-memory-utilization=0.75" in recommendation["command"]
            and any("same KV cache" in note for note in recommendation.get("notes", [])),
            f"0.75 and 0.95 booted the same KV cache but were not folded: {recommendation}",
        )
    for note in recommendation.get("notes", []):
        print(f"     note: {note}", flush=True)
    print(f"     kv_sizing: {recommendation.get('kv_sizing')}", flush=True)
    print(
        f"     recommended: {recommendation['command']} ({outcome.get('outcome')})",
        flush=True,
    )
    deleted = api.delete(f"/managed-targets/{target['id']}")
    check(deleted.status_code == 204, f"host target not deleted: {deleted.status_code}")
    return run_ids[-1]
