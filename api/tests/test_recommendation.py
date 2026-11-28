"""The recommendation's notes and KV sizing."""

from servtuner_api.managed_targets.spec import HostTargetSpec, ManagedTargetSpec
from servtuner_api.recommendation import recommend

SPEC = HostTargetSpec.model_validate(
    {
        "placement": "host",
        "launcher_url": "http://vllm-host.example:9123",
        "launcher_token": {"name": "launcher-token", "key": "token"},
        "endpoint": "http://vllm-host.example:8080/v1",
        "model": "Qwen/Qwen2.5-0.5B-Instruct",
        "metrics": {"job": "servtuner-external", "pod": "vllm-lan"},
    }
)
NOTE = (
    "vllm.gpu_memory_utilization=0.95 booted with the same KV cache as "
    "vllm.gpu_memory_utilization=0.75 (5312 blocks, sized by the memory free after warmup)"
)
SIZING = {"bound": "memory", "kv_cache_gib": 3.89, "requested_gib": 6.0}


def test_the_recommendation_carries_the_notes_and_the_winners_kv_sizing() -> None:
    knobs = {"vllm.gpu_memory_utilization": 0.75, "vllm.max_num_seqs": 32}
    recommendation = recommend(
        SPEC,
        {"preset": "chat-short@1", "slos": {"ttft_p95": "500ms"}},
        {"config_id": "c-1", "knobs": knobs},
        confirmed=False,
        knob_args=["--gpu-memory-utilization=0.75", "--max-num-seqs=32"],
        history=[],
        boot_results=[
            {"knobs": {"vllm.gpu_memory_utilization": 0.6}, "outcome": "feasible"},
            {
                "knobs": {"vllm.gpu_memory_utilization": 0.75},
                "outcome": "feasible",
                "kv_sizing": SIZING,
            },
        ],
        notes=[NOTE],
    )
    assert recommendation["notes"] == [NOTE]
    assert recommendation["kv_sizing"] == SIZING


def test_no_sizing_without_a_logged_boot() -> None:
    recommendation = recommend(
        SPEC,
        {"preset": "chat-short@1", "slos": {}},
        {"config_id": "c-1", "knobs": {"vllm.max_num_seqs": 32}},
        confirmed=True,
        knob_args=["--max-num-seqs=32"],
        history=[],
        boot_results=[],
    )
    assert (recommendation["notes"], recommendation["kv_sizing"]) == ([], None)


def test_the_command_starts_each_server_its_own_way() -> None:
    def command(spec: ManagedTargetSpec | HostTargetSpec, knob_args: list[str]) -> str:
        best: dict[str, object] = {"config_id": "c-1", "knobs": {"vllm.max_num_seqs": 32}}
        request = {"preset": "chat-short@1", "slos": {"ttft_p95": "500ms"}}
        return recommend(
            spec, request, best, confirmed=True, knob_args=knob_args, history=[], boot_results=[]
        )["command"]

    sglang = ManagedTargetSpec.model_validate(
        {
            "server": "sglang",
            "image": "lmsysorg/sglang:v0.5.23-runtime",
            "model": "Qwen/Qwen3-32B",
            "served_model_name": "qwen",
            "gpus": 1,
            "cpu": "8",
            "memory": "64Gi",
            "base_flags": ["--context-length=32768"],
            "model_cache": {"size": "100Gi"},
        }
    )
    assert command(sglang, ["--max-running-requests=64", "--tp-size=1"]) == (
        "python3 -m sglang.launch_server --model-path=Qwen/Qwen3-32B --served-model-name=qwen"
        " --context-length=32768 --max-running-requests=64 --tp-size=1"
    )
    assert command(SPEC, ["--max-num-seqs=32"]) == (
        "vllm serve Qwen/Qwen2.5-0.5B-Instruct --max-num-seqs=32"
    )
