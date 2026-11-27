"""TLS as two settings: rewrite the tls blocks servtuner 0.1 stored.

servtuner 0.2 replaced ``tls.mode`` (mtls, tls, platform) with ``tls.enabled`` and
``tls.clientCert``, and RunSpec ``servtuner.dev/v1`` with ``servtuner.dev/v2``. Saved targets,
managed targets and schedules are configuration that keeps being used, so their stored blocks are
rewritten here. Runs are history: their RunSpecs stay as submitted, and the api upgrades them when
it reads them (``legacy.py``).

The mapping is copied here, not imported, so this migration keeps doing what it did when it was
written whatever the application code becomes.

Revision ID: 0002
Revises: 0001
"""

import json
from collections.abc import Callable
from typing import Any

import sqlalchemy as sa
from alembic import op

revision = "0002"
down_revision = "0001"

LEGACY_MODES = ("mtls", "tls", "platform")

# A tls block, and where it is (for an error), to its converted form; None leaves it unchanged.
Converter = Callable[[dict[str, Any], str], dict[str, Any] | None]

# A managed target's transport, and where it is, to its converted form.
TransportConverter = Callable[[dict[str, Any], str], dict[str, Any]]


def _upgrade_tls(block: dict[str, Any]) -> dict[str, Any]:
    mode = block["mode"]
    upgraded: dict[str, Any] = {"enabled": mode != "platform"}
    for key, value in block.items():
        if key == "mode":
            continue
        if key == "secretRef" and mode == "mtls":
            upgraded["clientCert"] = {"secretRef": value}
        else:
            upgraded[key] = value
    return upgraded


def _downgrade_tls(block: dict[str, Any], where: str) -> dict[str, Any]:
    rest = {key: value for key, value in block.items() if key not in ("enabled", "clientCert")}
    if not block["enabled"]:
        return {"mode": "platform", **rest}
    if "clientCert" in block:
        if "caSecretRef" in rest:
            raise RuntimeError(
                f"{where}: a client certificate with caSecretRef has no servtuner 0.1 form"
            )
        return {"mode": "mtls", "secretRef": block["clientCert"]["secretRef"], **rest}
    return {"mode": "tls", **rest}


def _walk(node: Any, convert: Converter, where: str) -> Any:
    """A copy of node with each tls block converted."""
    if isinstance(node, list):
        return [_walk(child, convert, where) for child in node]
    if not isinstance(node, dict):
        return node
    walked: dict[str, Any] = {}
    for key, child in node.items():
        if key == "tls" and isinstance(child, dict):
            converted = convert(child, where)
            if converted is not None:
                walked[key] = converted
                continue
        walked[key] = _walk(child, convert, where)
    return walked


def _to_v2(block: dict[str, Any], _where: str) -> dict[str, Any] | None:
    return _upgrade_tls(block) if block.get("mode") in LEGACY_MODES else None


def _to_v1(block: dict[str, Any], where: str) -> dict[str, Any] | None:
    return _downgrade_tls(block, where) if isinstance(block.get("enabled"), bool) else None


def _transport_to_v2(transport: dict[str, Any], _where: str) -> dict[str, Any]:
    """A managed target's transport: platform is TLS off, tls is TLS with no client certificate."""
    if transport.get("mode") == "platform":
        return {"enabled": False}
    if transport.get("mode") == "tls":
        rest = {key: value for key, value in transport.items() if key != "mode"}
        return {"enabled": True, "client_auth": "none", **rest}
    return transport


def _transport_to_v1(transport: dict[str, Any], where: str) -> dict[str, Any]:
    if transport.get("enabled") is False:
        return {"mode": "platform"}
    if transport.get("enabled") is True:
        if transport.get("client_auth", "none") != "none":
            raise RuntimeError(f"{where}: client_auth has no servtuner 0.1 form")
        rest = {k: v for k, v in transport.items() if k not in ("enabled", "client_auth")}
        return {"mode": "tls", **rest}
    return transport


def _rewrite(
    convert: Converter, transport: TransportConverter, api_version: tuple[str, str]
) -> None:
    connection = op.get_bind()
    targets = sa.table("targets", sa.column("id"), sa.column("target", sa.JSON))
    for row in connection.execute(sa.select(targets.c.id, targets.c.target)).all():
        target = _walk(row.target, convert, f"target {row.id}")
        if target != row.target:
            connection.execute(targets.update().where(targets.c.id == row.id).values(target=target))
    managed = sa.table("managed_targets", sa.column("id"), sa.column("spec", sa.JSON))
    for row in connection.execute(sa.select(managed.c.id, managed.c.spec)).all():
        spec = dict(row.spec)
        if isinstance(spec.get("transport"), dict):
            spec["transport"] = transport(spec["transport"], f"managed target {row.id}")
        if spec != row.spec:
            connection.execute(managed.update().where(managed.c.id == row.id).values(spec=spec))
    schedules = sa.table("schedules", sa.column("id"), sa.column("spec_raw", sa.LargeBinary))
    for row in connection.execute(sa.select(schedules.c.id, schedules.c.spec_raw)).all():
        document = json.loads(row.spec_raw)
        if not isinstance(document, dict) or document.get("apiVersion") != api_version[0]:
            continue
        converted = _walk(document, convert, f"schedule {row.id}")
        converted["apiVersion"] = api_version[1]
        connection.execute(
            schedules.update()
            .where(schedules.c.id == row.id)
            .values(spec_raw=json.dumps(converted).encode())
        )


def upgrade() -> None:
    _rewrite(
        _to_v2,
        _transport_to_v2,
        ("servtuner.dev/v1", "servtuner.dev/v2"),
    )


def downgrade() -> None:
    _rewrite(_to_v1, _transport_to_v1, ("servtuner.dev/v2", "servtuner.dev/v1"))
