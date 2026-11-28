"""Reading vLLM's log: how much to read, and whether a failed boot was the configuration's fault
(infeasible) or a deploy defect."""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any, Literal

from servtuner_api.deployer.redact import redact_secrets

# T0: the root cause is logged well before the generic last
# line, so the classifier reads a window, not the tail.
LOG_TAIL_LINES = 200


# Lines of the log kept as evidence with a boot failure.
EXCERPT_LINES = 20


# The most log lines one read returns: servtuner launcher's most (MaxLogTail), kept for pods too.
MAX_LOG_TAIL_LINES = 1000


# The KV cache sizing lines come before the API server's startup lines, a few hundred lines
# into vLLM's log, so the sizing read takes all it can.
SIZING_LOG_TAIL_LINES = MAX_LOG_TAIL_LINES


# vLLM 0.34's sizing margin on CUDA (extensible_kv_cache.py):
# max(1 GiB, 0.75 x the allocated transient peak). ROCm's floor is 3 GiB.
KV_MARGIN_FLOOR_GIB = 1.0


KV_MARGIN_FRACTION = 0.75


ARGPARSE_EXIT_CODE = 2


FailureKind = Literal["infeasible", "render_bug", "deploy_defect", "sharing", "unknown"]


@dataclass(frozen=True)
class BootRule:
    rule: str
    kind: FailureKind
    pattern: re.Pattern[str]
    meaning: str


# vLLM 0.34's boot-failure messages, matched anywhere in the window. The order
# matters only for messages that could match two rules; the specific ones come first.
BOOT_RULES: tuple[BootRule, ...] = (
    BootRule(
        "unknown-flag",
        "render_bug",
        re.compile(r"error: unrecognized arguments: (.+)"),
        "vLLM refused a flag servtuner rendered: a rendering bug, not a configuration result",
    ),
    BootRule(
        "bad-flag-value",
        "render_bug",
        re.compile(
            r"error: argument (--[a-z0-9-]+).*: "
            r"(invalid (int|float|choice) value|expected one argument)"
        ),
        "vLLM refused a flag value servtuner rendered: a rendering bug",
    ),
    BootRule(
        "kv-too-small-for-max-model-len",
        "infeasible",
        re.compile(r"To serve at least one request with the model's max seq len \((\d+)\)"),
        "the KV cache cannot hold one request of max_model_len",
    ),
    BootRule(
        "no-kv-blocks",
        "infeasible",
        re.compile(r"No available memory for the cache blocks"),
        "after profiling, no GPU memory was left for KV cache blocks",
    ),
    BootRule(
        "not-enough-free-memory",
        "infeasible",
        re.compile(
            r"Free memory on device .* on startup is less than desired GPU memory utilization"
        ),
        "gpu_memory_utilization asks for more than is free",
    ),
    BootRule(
        "cuda-oom",
        "infeasible",
        re.compile(
            r"CUDA out of memory\. (Tried to allocate|Allocation was preemptively rejected)"
        ),
        "CUDA ran out of memory loading the weights or profiling",
    ),
    BootRule(
        "heads-not-divisible-by-tp",
        "infeasible",
        re.compile(
            r"Total number of attention heads \((\d+)\) must be divisible by tensor parallel size"
        ),
        "the model's attention heads do not split over this tensor-parallel size",
    ),
    BootRule(
        "more-gpus-than-visible",
        "infeasible",
        re.compile(r"World size \((\d+)\) is larger than the number of available GPUs"),
        "the parallelism asks for more GPUs than vLLM can see",
    ),
    BootRule(
        "shard-not-divisible",
        "infeasible",
        re.compile(r"(is not divisible by group_size=|\b\d+ is not divisible by \d+)"),
        "a layer does not shard evenly over this parallelism",
    ),
    BootRule(
        "memory-profiling-race",
        "sharing",
        re.compile(r"Error in memory profiling\. Initial free memory"),
        "another process changed GPU memory during profiling: the GPU is shared",
    ),
    # huggingface_hub's own words (file_download.py), when the weights are not there to read:
    # a seeded claim without this model, or a cluster with no way out.
    BootRule(
        "model-not-in-offline-cache",
        "deploy_defect",
        re.compile(r"Cannot find the requested files in the disk cache and outgoing traffic"),
        "the model is not in the seeded cache and this target may not download it"
        " (model_source pvc): seed the claim from the model's archive (INSTALL.md, 'Models')",
    ),
    BootRule(
        "hub-unreachable",
        "deploy_defect",
        re.compile(r"An error happened while trying to locate the file on the Hub"),
        "Hugging Face could not be reached to download the model: with no internet access,"
        " use model_source mirror or pvc (INSTALL.md, 'Models')",
    ),
    # FlashInfer 0.7.3's own words (flashinfer/jit/cubin_loader.py, artifacts.py): a precompiled
    # kernel the image lacks, which it could not or may not download.
    BootRule(
        "flashinfer-kernel-missing",
        "deploy_defect",
        re.compile(
            r"(Artifact not found locally: .*FLASHINFER_NO_DOWNLOAD is set"
            r"|Failed to fetch the checksum manifest for artifact pin)"
        ),
        "FlashInfer needs a precompiled kernel this image lacks and cannot download it: use an"
        " image whose flashinfer-cubin and flashinfer-jit-cache match its flashinfer (the stock"
        " vllm-openai image runs flashinfer download-kernels), or mirror FlashInfer's kernel"
        " repository and set FLASHINFER_CUBINS_REPOSITORY in the target's env",
    ),
    BootRule(
        "certificate-files",
        "deploy_defect",
        re.compile(r"(FileNotFoundError|ssl\.SSLError|SSLError).*"),
        "the certificate files could not be loaded",
    ),
)


# SGLang v0.5.23's boot-failure messages (from its source; checked live by #33's recording). The
# argument parser, the hub, offline and certificate rules are vLLM's own words too, so they are
# shared. SGLang reports a dead scheduler with its exit code, -9 meaning the OS OOM killer.
SGLANG_BOOT_RULES: tuple[BootRule, ...] = (
    BootRule(
        "unknown-flag",
        "render_bug",
        re.compile(r"error: unrecognized arguments: (.+)"),
        "SGLang refused a flag servtuner rendered: a rendering bug, not a configuration result",
    ),
    BootRule(
        "bad-flag-value",
        "render_bug",
        re.compile(
            r"error: argument (--[a-z0-9-]+).*: "
            r"(invalid (int|float|choice) value|expected one argument)"
        ),
        "SGLang refused a flag value servtuner rendered: a rendering bug",
    ),
    # model_executor/pool_configurator.py: the KV pool would hold no token.
    BootRule(
        "kv-pool-empty",
        "infeasible",
        re.compile(r"Not enough memory\. Please try to increase --mem-fraction-static"),
        "no GPU memory was left for the KV pool: mem_fraction_static is too low",
    ),
    # model_executor/runner_backend_utils: CUDA graph capture ran out of memory.
    BootRule(
        "cuda-graph-oom",
        "infeasible",
        re.compile(r"set --mem-fraction-static to a smaller value"),
        "CUDA graph capture ran out of memory: mem_fraction_static or cuda_graph_max_bs_decode"
        " is too high",
    ),
    BootRule(
        "cuda-oom",
        "infeasible",
        re.compile(r"(CUDA out of memory|torch\.OutOfMemoryError)"),
        "CUDA ran out of memory loading the weights or capturing graphs",
    ),
    # configs/model_config.py: a context length above the model's, without the override.
    BootRule(
        "context-length-too-long",
        "infeasible",
        re.compile(r"context_length \(\d+\) is greater than the derived context_length"),
        "context_length is longer than the model supports",
    ),
    BootRule(
        "scheduler-oom-killed",
        "deploy_defect",
        re.compile(r"scheduler died during initialization \(exit code: -9\)"),
        "the kernel killed SGLang's scheduler for host memory: raise the target's memory",
    ),
    *(
        rule
        for rule in BOOT_RULES
        if rule.rule in ("model-not-in-offline-cache", "hub-unreachable", "certificate-files")
    ),
)


@dataclass(frozen=True)
class BootFailure:
    kind: FailureKind
    rule: str
    message: str
    excerpt: list[str]
    exit_code: int | None = None


_STARTUP_FREE = re.compile(
    r"Free memory on device \((?P<free>[\d.]+)/(?P<total>[\d.]+) GiB\) on startup"
)


_AFTER_WARMUP = re.compile(
    r"Memory after warmup: (?P<free>[\d.]+) GiB free, (?P<committed>[\d.]+) GiB committed to the KV"
    r" cache, [\d.]+ GiB kept for transient activations \((?P<peak>[\d.]+) GiB allocated at peak,"
    r" (?P<growth>[\d.]+) GiB of torch reservation growth\); \d+ KV cache blocks"
    r" \((?P<kv>[\d.]+) GiB\) fit within the requested (?P<requested>[\d.]+) GiB"
)


# Developer pointers in log lines (pydantic's "For further information visit https://errors.
# pydantic.dev/..."), and any other link: a boot's reason is for the person reading the step, and
# the raw line stays in the failure's excerpt.
_FURTHER_INFORMATION = re.compile(r"\s*\(?For further information visit \S+?\)?(?=\s|$)")


_URL = re.compile(r"\s*\(?https?://\S+?\)?(?=\s|$)")


def user_facing(line: str) -> str:
    """A log line as a reason: without developer links, trimmed."""
    return _URL.sub("", _FURTHER_INFORMATION.sub("", line)).strip()


def parse_kv_sizing(log: str) -> dict[str, Any] | None:
    """Which limit sized vLLM's extensible KV cache (vLLM 0.34+), from its two sizing lines.

    The cache gets the smaller of gpu_memory_utilization x total less what vLLM uses besides
    the cache, and the memory free after warmup less a margin. None when the log lacks either
    line: an older vLLM, or the extensible cache is off.
    The figures are vLLM's, rounded to 0.01 GiB, so a boot right at the crossover may be
    named either way; its block count is what folds configurations.
    """
    startup = _STARTUP_FREE.search(log)
    warmup = _AFTER_WARMUP.search(log)
    if startup is None or warmup is None:
        return None
    free, committed = float(warmup["free"]), float(warmup["committed"])
    requested = float(warmup["requested"])
    margin = max(KV_MARGIN_FLOOR_GIB, KV_MARGIN_FRACTION * float(warmup["peak"]))
    memory_limit = free + committed - margin
    utilization_limit = requested - (float(startup["free"]) - free - committed)
    return {
        "bound": "utilization" if utilization_limit < memory_limit else "memory",
        "kv_cache_gib": float(warmup["kv"]),
        "requested_gib": requested,
        "memory_limit_gib": round(memory_limit, 2),
        "utilization_limit_gib": round(utilization_limit, 2),
    }


def classify_boot_failure(
    log: str, exit_code: int | None, reason: str | None, server: str = "vllm"
) -> BootFailure:
    """What a failed boot of vLLM (or SGLang, ``server``) says about the configuration."""
    name = "SGLang" if server == "sglang" else "vLLM"
    rules = SGLANG_BOOT_RULES if server == "sglang" else BOOT_RULES
    # Excerpts are stored and shown: SGLang's startup line carries its API key.
    lines = redact_secrets(log).splitlines()[-LOG_TAIL_LINES:]
    window = "\n".join(lines)
    if reason == "OOMKilled":
        return BootFailure(
            "deploy_defect",
            "host-oom-killed",
            f"the kernel killed {name} for host memory: raise the target's memory",
            lines[-EXCERPT_LINES:],
            exit_code,
        )
    for rule in rules:
        if rule.pattern.search(window) is None:
            continue
        at = next((i for i, line in enumerate(lines) if rule.pattern.search(line)), len(lines) - 1)
        excerpt = lines[max(0, at - EXCERPT_LINES // 2) : at + EXCERPT_LINES // 2]
        return BootFailure(rule.kind, rule.rule, rule.meaning, excerpt, exit_code)
    if exit_code == ARGPARSE_EXIT_CODE:
        return BootFailure(
            "render_bug",
            "argparse",
            f"{name}'s argument parser refused the args",
            lines[-EXCERPT_LINES:],
            exit_code,
        )
    return BootFailure(
        "unknown",
        "unrecognized",
        f"{name} exited without a known cause",
        lines[-EXCERPT_LINES:],
        exit_code,
    )
