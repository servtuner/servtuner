"""Secrets never leave the api in a model server's log."""

import pytest

from servtuner_api.deployer.boot import classify_boot_failure
from servtuner_api.deployer.redact import REDACTED, redact_secrets

KEY = "sentinel-secret-api-key"

# SGLang 0.5.23's startup line, recorded live (#33) with its key replaced by the sentinel and
# trimmed around the secret fields.
SGLANG_STARTUP = (
    "[2026-11-28 13:05:33] server_args={'model_path': 'Qwen/Qwen2.5-0.5B-Instruct', "
    "'ssl_keyfile_password': None, 'enable_ssl_refresh': False, "
    f"'api_key': '{KEY}', 'admin_api_key': None, 'served_model_name': "
    "'Qwen/Qwen2.5-0.5B-Instruct', 'enable_metrics': True}"
)


@pytest.mark.parametrize(
    "line",
    [
        SGLANG_STARTUP,
        f"INFO non-default args: {{'model': 'q', 'api_key': ['{KEY}']}}",
        f'{{"api_key": "{KEY}", "admin_api_key": "{KEY}"}}',
        f"ServerArgs(model_path='q', api_key='{KEY}')",
        f"python3 -m sglang.launch_server --api-key {KEY} --port 30000",
        f"vllm serve q --api-key={KEY}",
        f"--admin-api-key {KEY} --ssl-keyfile-password {KEY}",
    ],
)
def test_every_form_of_a_secret_is_redacted(line: str) -> None:
    redacted = redact_secrets(line)
    assert KEY not in redacted
    assert REDACTED in redacted


def test_the_rest_of_the_line_stays_readable() -> None:
    redacted = redact_secrets(SGLANG_STARTUP)
    assert f"'api_key': '{REDACTED}'" in redacted
    assert "'admin_api_key': None" in redacted
    assert "'enable_metrics': True" in redacted
    assert redact_secrets("--api-key --port 30000") == "--api-key --port 30000"


def test_a_boot_excerpt_never_carries_the_key() -> None:
    log = "\n".join(
        [
            SGLANG_STARTUP,
            "ValueError: Loaded weights leave no GPU memory for the KV cache under"
            " --mem-fraction-static=0.05.",
        ]
    )
    failure = classify_boot_failure(log, 137, None, server="sglang")
    assert all(KEY not in line for line in failure.excerpt)
