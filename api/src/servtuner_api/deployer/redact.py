"""Secrets out of a model server's log before servtuner shows or stores any of it.

SGLang prints every ServerArgs field at startup, its API keys included, even when servtuner passes
them in a config file only the server can read; a vLLM given ``--api-key`` echoes it among its
non-default args. The log panel and boot-failure excerpts carry that text to users, so every line
leaves the api through ``redact_secrets``.
"""

from __future__ import annotations

import re

# What a redacted value reads as.
REDACTED = "[redacted]"

# Settings whose values are secrets, as SGLang and vLLM name them.
_SECRET_NAMES = r"(?:admin_api_key|api_key|ssl_keyfile_password)"

# A dict or JSON entry: 'api_key': 'value' or "api_key": "value". None and null stay readable.
_MAPPING = re.compile(rf"""(['"]{_SECRET_NAMES}['"]\s*:\s*)(['"])(?:(?!\2).)*\2""")

# A list of them, as vLLM prints its non-default args: 'api_key': ['value', ...].
_MAPPING_LIST = re.compile(rf"""(['"]{_SECRET_NAMES}['"]\s*:\s*)\[[^\]]*\]""")

# A keyword argument or repr field: api_key='value'.
_KEYWORD = re.compile(rf"""(\b{_SECRET_NAMES}\s*=\s*)(['"])(?:(?!\2).)*\2""")

# A command-line flag: --api-key value or --api-key=value.
_FLAG = re.compile(r"(--(?:admin-api-key|api-key|ssl-keyfile-password)(?:=|\s+))(?!--)(\S+)")


def redact_secrets(text: str) -> str:
    """The log text with every secret value replaced by ``REDACTED``."""
    text = _MAPPING.sub(rf"\1\2{REDACTED}\2", text)
    text = _MAPPING_LIST.sub(rf"\1['{REDACTED}']", text)
    text = _KEYWORD.sub(rf"\1\2{REDACTED}\2", text)
    return _FLAG.sub(rf"\1{REDACTED}", text)
