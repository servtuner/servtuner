"""Tuning sessions: the loop of propose, deploy, boot, run, one transition per tick.

servtuner propose and the deployer are scripted stand-ins; the runs are real rows
created through RunService (so every RunSpec the session builds is validated)
and finished through the fake runner.
"""

import copy
import json
from collections.abc import Iterator, Mapping
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import Engine

from servtuner_api.app import create_app
from servtuner_api.managed_targets.service import ManagedTargetService
from servtuner_api.managed_targets.views import BootProbeView, ReadinessView
from servtuner_api.models import Comparison
from servtuner_api.models.generated.boot_results_schema import TuningBootResult
from servtuner_api.resources.experiments import ExperimentAnalysisResource, ExperimentAnalyze
from servtuner_api.runner import RunnerState
from servtuner_api.service import RunService
from servtuner_api.tuning.models import (
    SESSION_LABEL,
    SessionConflictError,
    SessionCreate,
    checked_space,
)
from servtuner_api.tuning.service import TuningService
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner
from tests.support.specs import example_bytes, run_spec_bytes
from tests.support.waiting import wait_until

PRESET = {
    "name": "chat-short",
    "version": 1,
    "description": "chat",
    "engines": ["loadgen"],
    "workload": {
        "api": "chat",
        "dataset": {
            "type": "synthetic",
            "input_tokens": {"mean": 256, "stddev": 64},
            "output_tokens": {"mean": 256, "stddev": 64},
        },
    },
    "slos": {"ttft_p95": "500ms"},
    "rps_hint": [{"gpu_class": "a100-80gb", "start_rps": 10}],
}
TARGET = {
    "name": "qwen",
    "namespace": "servtuner-tune-qwen",
    "spec": {
        "image": "vllm/vllm-openai:v0.34.0",
        "model": "Qwen/Qwen3-32B",
        "gpus": 2,
        "cpu": "16",
        "memory": "128Gi",
        "model_cache": {"size": "200Gi"},
    },
}
# A host target run by servtuner launcher (its token is set later, so none is needed here).
HOST_TARGET = {
    "name": "rtx",
    "namespace": "servtuner-external",
    "spec": {
        "placement": "host",
        "launcher_url": "http://vllm-host.example:9123",
        "endpoint": "http://vllm-host.example:8080/v1",
        "model": "Qwen/Qwen2.5-0.5B-Instruct",
        "metrics": {"job": "servtuner-external", "pod": "rtx"},
    },
}
HOST_SPACE: dict[str, Any] = {
    "knobs": [{"name": "vllm.max_num_seqs", "tier": "scheduler", "min": 16, "max": 64}]
}
SPACE: dict[str, Any] = {
    "knobs": [{"name": "vllm.tensor_parallel_size", "tier": "feasibility", "values": [1, 2]}]
}
BOOT = {
    "config_id": "c-0000aaaa",
    "action": "boot_probe",
    "knobs": {"vllm.tensor_parallel_size": 2},
    "rationale": {"kind": "feasibility", "text": "boot"},
}
RUN = {
    "config_id": "c-0000bbbb",
    "action": "run",
    "knobs": {"vllm.tensor_parallel_size": 2, "vllm.max_num_seqs": 128},
    "ladder": {"kind": "probe", "rps": [1, 2, 4]},
    "repeats": 2,
    "labels": {"servtuner.dev/tuning-config": "c-0000bbbb", "servtuner.dev/tuning-ladder": "probe"},
    "rationale": {"kind": "baseline", "text": "first run"},
}
WINNER = {
    "config_id": "c-0000bbbb",
    "knobs": RUN["knobs"],
    "objective_value": 6.5,
    "is_significant": True,
    "confidence": "high",
    "run_ids": ["r1", "r2"],
}
DONE = {
    "status": "done",
    "phase": "done",
    "done": {"outcome": "converged", "reason": "no step left", "winner": WINNER},
}


def propose(*proposed: dict[str, Any]) -> dict[str, Any]:
    return {"status": "propose", "phase": "search", "proposals": list(proposed)}


class ScriptedProposer:
    """servtuner propose: the next scripted answer per call (the first is create's dry run)."""

    def __init__(self, *answers: dict[str, Any]) -> None:
        self.answers = list(answers)
        self.calls: list[tuple[list[str], list[dict[str, Any]]]] = []

    def propose(
        self, space: Any, bundle_keys: list[str], boot_results: list[dict[str, Any]]
    ) -> dict[str, Any]:
        self.calls.append((bundle_keys, copy.deepcopy(boot_results)))
        return self.answers.pop(0)


class ScriptedManaged:
    """The managed target service with a scripted cluster: storage is real, deploys are not."""

    def __init__(self, real: ManagedTargetService) -> None:
        self.real = real
        self.readiness_state = "pending"
        self.readiness_reason = "loading"
        self.boot_result: dict[str, Any] | None = None
        self.deployed: list[dict[str, Any]] = []
        self.scaled_down = 0
        self.deploy_error: Exception | None = None
        # What a host's launcher reports for `vllm --version` (None: it cannot say).
        self.vllm_version: str | None = "0.34.0"

    def get(self, target_id: str) -> Any:
        return self.real.get(target_id)

    def resource(self, row: Any) -> Any:
        return self.real.resource(row)

    def installed_vllm_version(self, target_id: str) -> str | None:
        return self.vllm_version

    def deploy(self, target_id: str, raw: bytes) -> Any:
        if self.deploy_error is not None:
            raise self.deploy_error
        self.deployed.append(json.loads(raw)["knobs"])
        return self.real.get(target_id)

    def readiness(self, target_id: str) -> ReadinessView:
        return ReadinessView(state=self.readiness_state, reason=self.readiness_reason)  # type: ignore[arg-type]

    def boot_probe(self, target_id: str, raw: bytes) -> BootProbeView:
        assert json.loads(raw) == {"tokens_per_request": 512.0}  # chat-short: 256 in + 256 out
        result = (
            None if self.boot_result is None else TuningBootResult.model_validate(self.boot_result)
        )
        return BootProbeView(readiness=self.readiness(target_id), result=result)

    def scale_down(self, target_id: str) -> None:
        self.scaled_down += 1

    def knob_args(self, target_id: str, knobs: Mapping[str, Any]) -> list[str]:
        """As servtuner vllm-args spells these two knobs."""
        spelled = {
            "vllm.tensor_parallel_size": "--tensor-parallel-size",
            "vllm.max_num_seqs": "--max-num-seqs",
        }
        return [f"{spelled[name]}={value}" for name, value in sorted(knobs.items())]


class Clock:
    def __init__(self) -> None:
        self.now = datetime(2026, 11, 23, 12, 0, tzinfo=UTC)

    def __call__(self) -> datetime:
        return self.now


@pytest.fixture
def runner() -> FakeRunner:
    return FakeRunner()


@pytest.fixture
def service(engine: Engine, runner: FakeRunner, data_dir: Path) -> Iterator[RunService]:
    run_service = build_service(engine, runner, data_dir)
    run_service.managed_targets = ManagedTargetService(
        run_service._repository,  # noqa: SLF001 - the test assembles the service by hand
        ("servtuner-tune-qwen",),
    )
    yield run_service
    run_service.stop()


def tuning(
    service: RunService, proposer: ScriptedProposer, clock: Clock | None = None
) -> tuple[TuningService, ScriptedManaged]:
    managed = ScriptedManaged(service.managed_targets)
    service.tuning = TuningService(
        service._repository,  # noqa: SLF001
        service,
        managed,
        proposer,  # type: ignore[arg-type]
        lambda: [PRESET],
        None,
        clock or Clock(),
    )
    return service.tuning, managed


def new_session(service: RunService, **changes: Any) -> SessionCreate:
    target = service.managed_targets.create(json.dumps(TARGET).encode())
    body = {
        "managed_target_id": target.id,
        "preset": "chat-short@1",
        "slos": {"ttft_p95": "500ms"},
        "space": SPACE,
        "budget": {"max_runs": 20, "max_hours": 8},
    } | changes
    return SessionCreate.model_validate(body)


def finish_current(
    service: RunService, runner: FakeRunner, session_id: str, state: RunnerState
) -> str:
    run_id = service.tuning.get(session_id).step["current"]  # type: ignore[index]
    runner.finish(run_id, state, exit_code=0 if state is RunnerState.SUCCEEDED else 1)
    wait_until(lambda: service.get_run(run_id).status in ("succeeded", "failed"), "the run to end")  # type: ignore[union-attr]
    return str(run_id)


def test_a_session_boots_runs_with_a_retry_and_converges(
    service: RunService, runner: FakeRunner
) -> None:
    proposer = ScriptedProposer(propose(BOOT), propose(BOOT), propose(RUN), DONE)
    engine, managed = tuning(service, proposer)
    session = engine.create(new_session(service))
    assert session.status == "running" and session.experiment.startswith("tune-qwen-")

    assert engine.tick(session.id).step["phase"] == "deploy"  # type: ignore[index]
    assert engine.tick(session.id).step["phase"] == "booting"  # type: ignore[index]
    assert managed.deployed == [{"vllm.tensor_parallel_size": 2}]
    assert engine.tick(session.id).step["waiting"] == "loading"  # type: ignore[index]
    managed.boot_result = {
        "knobs": BOOT["knobs"],
        "outcome": "feasible",
        "num_gpu_blocks": 8192,
        "c_kv": 256.0,
    }
    managed.readiness_state = "ready"
    row = engine.tick(session.id)
    assert row.step == {"phase": "propose"}
    assert row.boot_results == [managed.boot_result]

    assert engine.tick(session.id).step["proposed"]["config_id"] == "c-0000bbbb"  # type: ignore[index]
    assert proposer.calls[2] == ([], [managed.boot_result])
    engine.tick(session.id)  # deploy
    assert engine.tick(session.id).step["phase"] == "running"  # type: ignore[index]

    row = engine.tick(session.id)  # repeat 0 starts
    first = service.get_run(row.step["current"])  # type: ignore[index]
    spec = json.loads(first.spec_raw)  # type: ignore[union-attr]
    assert spec["labels"][SESSION_LABEL] == session.id.lower()
    assert spec["labels"]["servtuner.dev/tuning-ladder"] == "probe"
    # The target's page lists the session's runs by this label (ManagedTargetResource.run_labels).
    assert spec["labels"]["servtuner.managed-target"] == session.managed_target_id.lower()
    assert spec["knobs"] == RUN["knobs"]
    assert spec["workload"] == {"preset": "chat-short@1", "streaming": True}
    assert [stage["rate"] for stage in spec["load"]["stages"]] == [1, 2, 4]
    assert spec["target"]["endpoint"] == "http://qwen.servtuner-tune-qwen.svc:8000/v1"
    assert engine.tick(session.id).step["current"] == first.id  # type: ignore[index,union-attr]  # still running

    finish_current(service, runner, session.id, RunnerState.SUCCEEDED)
    assert engine.tick(session.id).step["repeat"] == 1  # type: ignore[index]
    engine.tick(session.id)  # repeat 1 starts
    failed = finish_current(service, runner, session.id, RunnerState.FAILED)
    assert engine.tick(session.id).step["retried"] is True  # type: ignore[index]
    engine.tick(session.id)  # the retry starts
    finish_current(service, runner, session.id, RunnerState.SUCCEEDED)
    row = engine.tick(session.id)
    assert row.step == {"phase": "propose"}
    assert row.runs_started == 3
    assert failed in row.history[-1]["run_ids"]

    row = engine.tick(session.id)
    assert row.status == "done"
    assert row.outcome["outcome"] == "converged"  # type: ignore[index]
    recommendation = row.outcome["recommendation"]  # type: ignore[index]
    assert recommendation["command"] == (
        "vllm serve Qwen/Qwen3-32B --max-num-seqs=128 --tensor-parallel-size=2"
    )
    assert recommendation["confirmed"] is True
    assert recommendation["evidence"]["confidence"] == "high"
    assert recommendation["measured_for"]["preset"] == "chat-short@1"
    assert [step["action"] for step in recommendation["winner_steps"]] == ["run"]
    assert recommendation["winner_steps"][0]["rationale"]["kind"] == "baseline"
    containers = recommendation["deployment_patch"]["spec"]["template"]["spec"]["containers"]
    assert containers[0]["args"] == recommendation["args"]
    assert len(proposer.calls[3][0]) == 2  # the two succeeded runs' bundles
    assert managed.scaled_down == 1
    # No bench binary here: the comparison cannot run, and the outcome says so.
    assert "pareto_error" in row.outcome  # type: ignore[operator]


def test_one_session_at_a_time(service: RunService) -> None:
    engine, _ = tuning(service, ScriptedProposer(propose(BOOT), propose(BOOT)))
    first = engine.create(new_session(service))
    with pytest.raises(SessionConflictError, match="one at a time"):
        engine.create(SessionCreate.model_validate(new_session_body(first.managed_target_id)))


def test_cancel_cancels_the_running_run_before_freeing_the_gpu(
    service: RunService, runner: FakeRunner
) -> None:
    engine, managed = tuning(service, ScriptedProposer(propose(RUN), propose(RUN)))
    session = engine.create(new_session(service))  # the first answer is create's dry run
    engine.tick(session.id)  # propose
    engine.tick(session.id)  # deploy
    managed.readiness_state = "ready"
    assert engine.tick(session.id).step["phase"] == "running"  # type: ignore[index]
    run_id = engine.tick(session.id).step["current"]  # type: ignore[index]
    row = engine.cancel(session.id)
    assert row.status == "cancelled"
    assert runner.cancelled == [run_id]
    assert service.get_run(run_id).status == "cancelled"  # type: ignore[union-attr]
    assert managed.scaled_down == 1


def test_manual_mode_waits_for_approval(service: RunService) -> None:
    engine, managed = tuning(service, ScriptedProposer(propose(BOOT), propose(BOOT)))
    session = engine.create(new_session(service, mode="manual"))
    row = engine.tick(session.id)
    assert row.status == "awaiting_approval"
    assert (
        engine.tick(session.id).status == "awaiting_approval"
    )  # a tick does nothing while it waits
    assert managed.deployed == []
    assert engine.approve(session.id).step["phase"] == "deploy"  # type: ignore[index]
    engine.tick(session.id)
    assert managed.deployed == [BOOT["knobs"]]


def test_a_finished_session_carries_its_pareto_set(service: RunService) -> None:
    """servtuner compare over the session's experiment; its Pareto set lands in the outcome."""
    comparison = json.loads(example_bytes("comparison", "valid", "winner-with-repeats"))
    asked: list[tuple[str, ExperimentAnalyze]] = []

    def analyze(name: str, request: ExperimentAnalyze) -> ExperimentAnalysisResource:
        asked.append((name, request))
        return ExperimentAnalysisResource(
            id="01JANALYSIS000000000000000",
            experiment=name,
            created_at=datetime(2026, 11, 25, tzinfo=UTC),
            objective="slo_max_rps",
            run_ids=["r1"],
            comparison=Comparison.model_validate(comparison),
        )

    service.experiments.analyze = analyze  # type: ignore[method-assign]
    leader = {"config_id": "c-0000bbbb", "knobs": RUN["knobs"], "slo_max_rps": 12.0}
    engine, _ = tuning(service, ScriptedProposer(propose(BOOT), propose(RUN) | {"leader": leader}))
    session = engine.create(new_session(service, budget={"max_runs": 1, "max_hours": 8}))
    row = engine.tick(session.id)
    assert asked == [(row.experiment, ExperimentAnalyze(objective=None))]
    assert row.outcome["comparison"] == {  # type: ignore[index]
        "analysis_id": "01JANALYSIS000000000000000",
        "objective": "slo_max_rps",
    }
    assert row.outcome["pareto"] == [  # type: ignore[index]
        {
            "knobs": {"bifrost.concurrency": 100},
            "run_ids": comparison["configurations"][0]["run_ids"],
            "objective_value": 210,
            "ttft_p95_seconds": 0.081,
        },
        {
            "knobs": {"bifrost.concurrency": 400},
            "run_ids": comparison["configurations"][1]["run_ids"],
            "objective_value": 330,
            "ttft_p95_seconds": 0.079,
        },
    ]


def test_a_session_reuses_an_earlier_sessions_evidence(service: RunService) -> None:
    """The earlier session's boots seed servtuner propose; a feasible boot must name every
    feasibility knob of the new space, an infeasible one always carries over."""
    clock = Clock()
    clock.now = datetime.now(UTC) + timedelta(hours=1)  # after the target's real creation time
    proposer = ScriptedProposer(*[propose(BOOT)] * 6)
    engine, _ = tuning(service, proposer, clock)
    first_body = new_session(service)
    first = engine.create(first_body)
    engine.cancel(first.id)
    feasible = {"knobs": {"vllm.tensor_parallel_size": 2}, "outcome": "feasible", "c_kv": 40.0}
    infeasible = {"knobs": {"vllm.tensor_parallel_size": 4}, "outcome": "infeasible"}
    unnamed = {"knobs": {"vllm.max_num_seqs": 64}, "outcome": "feasible", "c_kv": 40.0}
    service._repository.update_tuning_session(  # noqa: SLF001 - the evidence a session left
        first.id, boot_results=[feasible, infeasible, unnamed]
    )
    clock.now += timedelta(minutes=5)
    reusing = first_body.model_copy(update={"reuse_from": [first.id]})
    second = engine.create(reusing)
    assert second.boot_results == [feasible, infeasible]
    assert proposer.calls[-1] == ([], [feasible, infeasible]), "the dry run sees the evidence"

    other_slos = reusing.model_copy(update={"slos": {"ttft_p95": "1s"}})
    with pytest.raises(SessionConflictError, match="SLOs differ"):
        engine.create(other_slos)
    engine.cancel(second.id)
    repository = service._repository  # noqa: SLF001 - the test rewrites what was stored
    repository.update_tuning_session(first.id, request=first.request | {"preset": "chat-long@1"})
    with pytest.raises(SessionConflictError, match="preset chat-long@1, not chat-short@1"):
        engine.create(reusing)
    repository.update_tuning_session(first.id, request=first.request)
    repository.update_managed_target(  # an edit after the session started
        first.managed_target_id, updated_at=clock.now + timedelta(minutes=1)
    )
    with pytest.raises(SessionConflictError, match="edited since it started"):
        engine.create(reusing)


def test_a_reusing_session_proposes_from_the_earlier_runs_too(
    service: RunService, runner: FakeRunner
) -> None:
    clock = Clock()
    clock.now = datetime.now(UTC) + timedelta(hours=1)
    proposer = ScriptedProposer(*[propose(BOOT)] * 4)
    engine, _ = tuning(service, proposer, clock)
    first_body = new_session(service)
    first = engine.create(first_body)
    engine.cancel(first.id)
    earlier = service.create_run(run_spec_bytes(labels={SESSION_LABEL: first.id.lower()}))
    runner.finish(earlier.id, RunnerState.SUCCEEDED, exit_code=0)
    wait_until(lambda: service.get_run(earlier.id).status == "succeeded", "the run to end")  # type: ignore[union-attr]
    clock.now += timedelta(minutes=5)
    second = engine.create(first_body.model_copy(update={"reuse_from": [first.id]}))
    earlier_bundle = service.bundle_key(service.get_run(earlier.id))  # type: ignore[arg-type]
    assert proposer.calls[-1][0] == [earlier_bundle], "the dry run sees the earlier run"
    engine.tick(second.id)  # propose
    assert proposer.calls[-1][0] == [earlier_bundle], "and so does every proposal"


def test_a_host_session_is_reused_only_on_the_same_vllm(service: RunService) -> None:
    """A host's vLLM can change without its target changing, so the earlier boots' recorded
    version must equal what the host's launcher reports now."""
    clock = Clock()
    clock.now = datetime.now(UTC) + timedelta(hours=1)
    engine, managed = tuning(service, ScriptedProposer(*[propose(BOOT)] * 6), clock)
    host = service.managed_targets.create(json.dumps(HOST_TARGET).encode())
    body = SessionCreate.model_validate(new_session_body(host.id) | {"space": HOST_SPACE})
    first = engine.create(body)
    engine.cancel(first.id)
    reusing = body.model_copy(update={"reuse_from": [first.id]})
    with pytest.raises(SessionConflictError, match="recorded no vLLM version"):
        engine.create(reusing)

    boots = [
        {"knobs": {"vllm.max_num_seqs": 32}, "outcome": "feasible", "vllm_version": "0.33.0"},
        {"knobs": {"vllm.max_num_seqs": 512}, "outcome": "infeasible"},
    ]
    service._repository.update_tuning_session(first.id, boot_results=boots)  # noqa: SLF001
    with pytest.raises(SessionConflictError, match="ran vLLM 0.33.0; the host now has 0.34.0"):
        engine.create(reusing)
    managed.vllm_version = None
    with pytest.raises(SessionConflictError, match="did not report its vLLM version"):
        engine.create(reusing)
    managed.vllm_version = "0.33.0"
    assert engine.create(reusing).boot_results == boots


def test_the_budget_ends_the_session_with_the_leader(service: RunService) -> None:
    leader = {"config_id": "c-0000bbbb", "knobs": RUN["knobs"], "slo_max_rps": 12.0}
    over = propose(RUN) | {"leader": leader}
    engine, managed = tuning(service, ScriptedProposer(propose(BOOT), over))
    session = engine.create(new_session(service, budget={"max_runs": 1, "max_hours": 8}))
    row = engine.tick(session.id)
    assert row.status == "done"
    assert row.outcome["outcome"] == "budget"  # type: ignore[index]
    assert row.outcome["leader"] == leader  # type: ignore[index]
    assert row.outcome["recommendation"]["confirmed"] is False  # type: ignore[index]
    assert managed.scaled_down == 1


def test_an_infeasible_run_step_becomes_a_boot_result(service: RunService) -> None:
    engine, managed = tuning(service, ScriptedProposer(propose(BOOT), propose(RUN)))
    session = engine.create(new_session(service))
    engine.tick(session.id)
    engine.tick(session.id)
    managed.readiness_state, managed.readiness_reason = "infeasible", "no-kv-blocks: no memory"
    row = engine.tick(session.id)
    assert row.boot_results == [
        {"knobs": RUN["knobs"], "outcome": "infeasible", "reason": "no-kv-blocks: no memory"}
    ]
    assert row.step == {"phase": "propose"} and row.status == "running"


def test_a_failed_deploy_or_a_stuck_one_fails_the_session(service: RunService) -> None:
    clock = Clock()
    engine, managed = tuning(service, ScriptedProposer(propose(BOOT), propose(RUN)), clock)
    session = engine.create(new_session(service))
    engine.tick(session.id)
    engine.tick(session.id)
    clock.now += timedelta(minutes=46)
    row = engine.tick(session.id)
    assert row.status == "failed" and "not ready after 45 min" in row.outcome["reason"]  # type: ignore[index]
    assert managed.scaled_down == 1


def test_repeated_errors_fail_the_session_and_cancel_frees_the_gpu(service: RunService) -> None:
    engine, managed = tuning(service, ScriptedProposer(propose(BOOT), propose(BOOT), propose(BOOT)))
    session = engine.create(new_session(service))
    engine.tick(session.id)
    managed.deploy_error = RuntimeError("the API server is down")
    for _ in range(4):
        assert engine.tick(session.id).status == "running"
    row = engine.tick(session.id)
    assert row.status == "failed" and "5 ticks in a row failed" in row.outcome["reason"]  # type: ignore[index]

    second = engine.create(SessionCreate.model_validate(new_session_body(row.managed_target_id)))
    cancelled = engine.cancel(second.id)
    assert cancelled.status == "cancelled" and managed.scaled_down == 2
    with pytest.raises(SessionConflictError):
        engine.cancel(second.id)


def new_session_body(target_id: str) -> dict[str, Any]:
    return {
        "managed_target_id": target_id,
        "preset": "chat-short@1",
        "slos": {"ttft_p95": "500ms"},
        "space": SPACE,
        "budget": {"max_runs": 20, "max_hours": 8},
    }


def test_the_api_starts_lists_and_cancels_sessions(service: RunService) -> None:
    engine, _ = tuning(service, ScriptedProposer(propose(BOOT), propose(BOOT)))
    target = service.managed_targets.create(json.dumps(TARGET).encode())
    with TestClient(create_app(lambda: service)) as client:
        created = client.post("/tuning-sessions", json=new_session_body(target.id))
        assert created.status_code == 201, created.text
        session = created.json()
        assert session["status"] == "running" and session["step"] == {"phase": "propose"}
        assert client.post("/tuning-sessions", json=new_session_body(target.id)).status_code == 409
        assert [s["id"] for s in client.get("/tuning-sessions").json()] == [session["id"]]
        assert (
            client.get(f"/tuning-sessions/{session['id']}").json()["experiment"]
            == session["experiment"]
        )
        assert client.post(f"/tuning-sessions/{session['id']}/approve").status_code == 409
        assert (
            client.post(f"/tuning-sessions/{session['id']}/cancel").json()["status"] == "cancelled"
        )
        assert client.get("/tuning-sessions/nope").status_code == 404
        bad = new_session_body(target.id) | {"preset": "chat-tiny"}
        assert client.post("/tuning-sessions", json=bad).status_code == 422


def test_a_users_changes_to_a_target_wait_for_its_session(service: RunService) -> None:
    """Deploy, scale-down, edit and delete would pull the target out from under the session."""
    tuning(service, ScriptedProposer(propose(BOOT)))
    target = service.managed_targets.create(json.dumps(TARGET).encode())
    route = f"/managed-targets/{target.id}"
    with TestClient(create_app(lambda: service)) as client:
        session = client.post("/tuning-sessions", json=new_session_body(target.id)).json()
        refused = [
            client.post(f"{route}/deploy", json={"knobs": {}}),
            client.post(f"{route}/scale-down"),
            client.put(route, json=TARGET),
            client.delete(route),
        ]
        assert [response.status_code for response in refused] == [409] * 4
        assert session["id"] in refused[0].json()["detail"]
        client.post(f"/tuning-sessions/{session['id']}/cancel")
        assert client.put(route, json=TARGET).status_code == 200
        assert client.delete(route).status_code == 204


def test_the_real_bench_propose_starts_with_boot_probes(bench_binary: Path, data_dir: Path) -> None:
    """The Proposer and servtuner propose agree on the inputs and the proposal's shape."""
    from servtuner_api.bundles import FileBundleStore
    from servtuner_api.tuning.proposer import Proposer

    proposal = Proposer(bench_binary, FileBundleStore(data_dir)).propose(SPACE, [], [])
    assert proposal["status"] == "propose"
    assert {p["action"] for p in proposal["proposals"]} == {"boot_probe"}
    assert {p["knobs"]["vllm.tensor_parallel_size"] for p in proposal["proposals"]} == {1, 2}


def test_a_space_ranks_by_an_objective_the_targets_runs_can_compute() -> None:
    """A host without hardware ranks by slo_max_rps; a per-GPU or cost objective it
    cannot compute is refused before the first run."""
    knobs = [{"name": "vllm.max_num_seqs", "tier": "scheduler", "min": 16, "max": 256}]
    bare_host = ["slo_max_rps", "p95_ttft_at_target_rps"]
    assert checked_space({"knobs": knobs}, bare_host) == {
        "knobs": knobs,
        "objective": "slo_max_rps",
    }
    kept = checked_space({"knobs": knobs, "objective": "p95_ttft_at_target_rps"}, bare_host)
    assert kept["objective"] == "p95_ttft_at_target_rps"
    with pytest.raises(
        SessionConflictError, match="can rank by slo_max_rps, p95_ttft_at_target_rps"
    ):
        checked_space({"knobs": knobs, "objective": "slo_rps_per_gpu"}, bare_host)
