"""The chart README's environment table must document every variable the api reads."""

import re
from pathlib import Path

from servtuner_api import datadir
from servtuner_api.settings import env, models

README = Path(__file__).resolve().parents[2] / "deploy" / "helm" / "servtuner" / "README.md"
ENV_NAME = re.compile(r'"((?:SERVTUNER|SSL)_[A-Z0-9_]+)"')
BACKTICKED = re.compile(r"`(_?[A-Z][A-Z0-9_]*)`")


def read_by_the_api() -> set[str]:
    sources = [Path(module.__file__ or "").read_text() for module in (env, datadir)]
    literal_names = {name for source in sources for name in ENV_NAME.findall(source)}
    connection_names = {env.connection_env_name(connection) for connection in models.Connection}
    # servtuner 0.1's names are read only to be refused, so they are not part of the contract.
    legacy = {env.LEGACY_ENV_TLS_MODE} | {env.legacy_hop_env_name(hop) for hop in models.Connection}
    return (literal_names | connection_names) - legacy


def documented_names() -> set[str]:
    """Names in the table; a `_SUFFIX` after a full name abbreviates a sibling of it."""
    names: set[str] = set()
    for row in README.read_text().splitlines():
        if not row.startswith("| `"):
            continue
        first_cell = row.split("|")[1]
        full_names = [
            token for token in BACKTICKED.findall(first_cell) if not token.startswith("_")
        ]
        if not full_names:
            continue  # a row that documents something other than an env var
        names.update(full_names)
        stem = full_names[0].split("_")
        for suffix in (token for token in BACKTICKED.findall(first_cell) if token.startswith("_")):
            names.update("_".join(stem[:length]) + suffix for length in range(1, len(stem)))
    return names


def test_every_variable_the_api_reads_is_documented() -> None:
    undocumented = read_by_the_api() - documented_names()
    assert not undocumented, f"missing from deploy/helm/servtuner/README.md: {sorted(undocumented)}"


def test_the_check_sees_the_new_names() -> None:
    assert {
        "SERVTUNER_ENGINE_OVERRIDE",
        "SERVTUNER_TLS_CLIENT_CA_FILE",
        "SERVTUNER_TLS_ENABLED",
        "SERVTUNER_TLS_CLIENT_AUTH",
        "SERVTUNER_TLS_CONNECTION_POSTGRES",
        "SERVTUNER_DATABASE_AUTH",
    } <= (read_by_the_api())
    assert "SERVTUNER_TLS_MODE" not in read_by_the_api()
