"""Reading what servtuner 0.1 wrote, in servtuner 0.2's shape.

servtuner 0.1 wrote RunSpecs as ``servtuner.dev/v1``, with ``tls.mode`` mtls, tls or platform on
every hop, and recorded each hop's transport in ``environment.json`` as that mode string (bundle
format 1). 0.2 replaced the mode with ``tls.enabled`` and ``tls.clientCert`` (RunSpec
``servtuner.dev/v2``, bundle format 2).

A RunSpec someone submits must be v2: bench rejects v1 and names the replacement. Data servtuner
wrote itself is still read: a stored run's RunSpec and a bundle's ``environment.json`` are upgraded
here when they are read, and never rewritten. bench's ``spec/legacy.go`` does the same for bench;
``tests/test_legacy.py`` checks the two agree on the same fixtures.
"""

import copy
import json
from typing import Any

API_VERSION_V1 = "servtuner.dev/v1"

API_VERSION_V2 = "servtuner.dev/v2"

# What each 0.1 mode recorded: TLS, and whether servtuner presented a client certificate.
LEGACY_TRANSPORTS = {
    "mtls": {"tls": True, "client_cert": True},
    "tls": {"tls": True, "client_cert": False},
    "platform": {"tls": False, "client_cert": False},
}


def upgrade_legacy_tls(block: dict[str, Any]) -> dict[str, Any] | None:
    """A 0.1 tls block in 0.2's shape: mtls with secretRef S becomes enabled true with
    clientCert {secretRef: S}, tls becomes enabled true (caSecretRef kept), platform becomes
    enabled false. Other keys are kept, so an invalid block stays invalid. None for an unknown
    mode."""
    mode = block.get("mode")
    if mode not in LEGACY_TRANSPORTS:
        return None
    upgraded: dict[str, Any] = {"enabled": mode != "platform"}
    for key, value in block.items():
        if key == "mode":
            continue
        if key == "secretRef" and mode == "mtls":
            upgraded["clientCert"] = {"secretRef": value}
        else:
            upgraded[key] = value
    return upgraded


def upgrade_tls_blocks(node: Any) -> Any:
    """A copy of node with every 0.1 tls block (a ``tls`` key holding a ``mode``) upgraded."""
    if isinstance(node, list):
        return [upgrade_tls_blocks(child) for child in node]
    if not isinstance(node, dict):
        return node
    upgraded: dict[str, Any] = {}
    for key, child in node.items():
        if key == "tls" and isinstance(child, dict) and "mode" in child:
            block = upgrade_legacy_tls(child)
            if block is not None:
                upgraded[key] = block
                continue
        upgraded[key] = upgrade_tls_blocks(child)
    return upgraded


def is_legacy_run_spec(document: object) -> bool:
    return isinstance(document, dict) and document.get("apiVersion") == API_VERSION_V1


def upgrade_legacy_run_spec(document: Any) -> Any:
    """A RunSpec servtuner stored (a run, a follow-up's source) in the current shape: a v1
    document becomes v2, with each tls block upgraded. Any other document comes back unchanged.
    Never applied to a RunSpec someone submits."""
    if not is_legacy_run_spec(document):
        return document
    upgraded = upgrade_tls_blocks(document)
    upgraded["apiVersion"] = API_VERSION_V2
    return upgraded


def upgrade_legacy_run_spec_raw(spec_raw: bytes) -> bytes:
    """upgrade_legacy_run_spec for stored bytes: a v2 RunSpec's bytes come back untouched."""
    try:
        document = json.loads(spec_raw)
    except ValueError:
        return spec_raw
    if not is_legacy_run_spec(document):
        return spec_raw
    return json.dumps(upgrade_legacy_run_spec(document)).encode()


def upgrade_legacy_environment(document: Any) -> Any:
    """An environment.json from a bundle of format 1, where each hop's transport was a mode
    string, with each mode turned into {tls, client_cert}. Any other document comes back
    unchanged."""
    if not isinstance(document, dict) or not isinstance(document.get("transport"), dict):
        return document
    hops = document["transport"]
    if not any(isinstance(mode, str) and mode in LEGACY_TRANSPORTS for mode in hops.values()):
        return document
    upgraded = copy.deepcopy(document)
    for hop, mode in hops.items():
        if isinstance(mode, str) and mode in LEGACY_TRANSPORTS:
            upgraded["transport"][hop] = dict(LEGACY_TRANSPORTS[mode])
    return upgraded


# Bundle documents read by schema name that may need upgrading.
BUNDLE_UPGRADES = {
    "runspec": upgrade_legacy_run_spec,
    "environment": upgrade_legacy_environment,
}


def upgrade_bundle_document(schema_name: str, document: Any) -> Any:
    """A bundle document in the current shape, by schema; other schemas come back unchanged."""
    upgrade = BUNDLE_UPGRADES.get(schema_name)
    return upgrade(document) if upgrade else document
