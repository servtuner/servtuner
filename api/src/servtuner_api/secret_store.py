"""Where credential values live, and how a run gets them.

A saved target's keys are Secrets: Kubernetes Secrets in the runs namespace, or,
for local development without a cluster, files under the data directory
(``SERVTUNER_SECRET_STORE=file``). Either way a RunSpec only ever names them
(root CLAUDE.md rule 4), and the api never returns a value over HTTP: ``read``
exists for ``materialize``, which hands a subprocess run, or a target check,
the files bench reads with ``--secrets-dir``.

Nothing in this module logs a value or puts one in an exception message.
"""

import base64
import logging
import os
import shutil
from collections.abc import Mapping
from pathlib import Path
from typing import Protocol

from lightkube import Client
from lightkube.core.exceptions import ApiError
from lightkube.models.meta_v1 import ObjectMeta
from lightkube.resources.core_v1 import Secret

from servtuner_api.kubernetes_decoders import prepare_decoders

logger = logging.getLogger(__name__)

MANAGED_BY_LABEL = "app.kubernetes.io/managed-by"
MANAGED_BY = "servtuner"
TARGET_ID_LABEL = "servtuner.dev/target-id"
FIELD_MANAGER = "servtuner-api"
HTTP_NOT_FOUND = 404

# 0700 for directories and 0400 for materialized files: only the api's own user
# (and bench, its child) can read them. Stored files are 0600 so they can be replaced.
PRIVATE_DIR_MODE = 0o700
MATERIALIZED_FILE_MODE = 0o400
STORED_FILE_MODE = 0o600


# The RunSpec keys whose value names a Secret.
SECRET_REF_KEYS = frozenset({"secretRef", "caSecretRef"})


def secret_names_in(spec: object) -> list[str]:
    """Every Secret a RunSpec (or a part of one) names, sorted and once each.

    A name is any ``secretRef`` (credentials, mtls client certificates) or
    ``caSecretRef`` (a tls hop's private CA).
    """
    found: set[str] = set()

    def walk(node: object) -> None:
        if isinstance(node, dict):
            for key, value in node.items():
                if key in SECRET_REF_KEYS and isinstance(value, dict):
                    name = value.get("name")
                    if isinstance(name, str) and name:
                        found.add(name)
                walk(value)
        elif isinstance(node, list):
            for item in node:
                walk(item)

    walk(spec)
    return sorted(found)


class SecretStore(Protocol):
    def put(self, name: str, data: Mapping[str, str], target_id: str) -> None:
        """Create or wholly replace the Secret ``name`` (keys not in ``data`` are removed)."""
        ...

    def read(self, name: str) -> dict[str, str] | None:
        """The Secret's keys and values, or None when it does not exist. Never logged."""
        ...

    def delete(self, name: str) -> None:
        """Remove the Secret; a missing one is not an error."""
        ...


class KubernetesSecretStore:
    """Secrets in the runs namespace; the api's Role already allows it (rbac.yaml)."""

    def __init__(self, namespace: str, client: Client | None = None) -> None:
        prepare_decoders()
        self._namespace = namespace
        self._client = client or Client(namespace=namespace, field_manager=FIELD_MANAGER)

    def put(self, name: str, data: Mapping[str, str], target_id: str) -> None:

        secret = Secret(
            apiVersion="v1",
            kind="Secret",
            metadata=ObjectMeta(
                name=name,
                namespace=self._namespace,
                labels={MANAGED_BY_LABEL: MANAGED_BY, TARGET_ID_LABEL: target_id.lower()},
            ),
            type="Opaque",
            # data, not stringData: server-side apply then owns each key, so a key
            # left out of a replacement (token -> username and password) is removed.
            data={key: base64.b64encode(value.encode()).decode() for key, value in data.items()},
        )
        self._client.apply(secret, field_manager=FIELD_MANAGER, force=True)

    def read(self, name: str) -> dict[str, str] | None:

        try:
            secret = self._client.get(Secret, name)
        except ApiError as error:
            if error.status.code == HTTP_NOT_FOUND:
                return None
            raise
        return {key: base64.b64decode(value).decode() for key, value in (secret.data or {}).items()}

    def delete(self, name: str) -> None:

        try:
            self._client.delete(Secret, name)
        except ApiError as error:
            if error.status.code != HTTP_NOT_FOUND:
                raise


class FileSecretStore:
    """Local development only: ``<root>/<name>/<key>``, 0600, the layout bench reads."""

    def __init__(self, root: Path) -> None:
        self._root = root
        logger.warning(
            "secret store is 'file' (%s): keys are plain files on this disk; use it for "
            "local development only",
            root,
        )

    def put(self, name: str, data: Mapping[str, str], target_id: str) -> None:  # noqa: ARG002
        _check_plain_name(name)
        self._root.mkdir(mode=PRIVATE_DIR_MODE, parents=True, exist_ok=True)
        staging = self._root / f".{name}.new"
        shutil.rmtree(staging, ignore_errors=True)
        _write_files(staging, data, STORED_FILE_MODE)
        final = self._root / name
        shutil.rmtree(final, ignore_errors=True)
        staging.rename(final)

    def read(self, name: str) -> dict[str, str] | None:
        _check_plain_name(name)
        return _read_files(self._root / name)

    def delete(self, name: str) -> None:
        _check_plain_name(name)
        shutil.rmtree(self._root / name, ignore_errors=True)


def materialize(
    names: list[str], directory: Path, store: SecretStore | None, mounted_dir: Path | None
) -> list[str]:
    """Write each named Secret to ``<directory>/<name>/<key>`` for ``bench --secrets-dir``.

    A Secret comes from the store, or else from ``mounted_dir`` (Secrets the
    chart mounts, ``subprocess.secrets``). Returns the names found in neither:
    bench then fails at resolve with "secret unavailable", as before.
    """
    directory.mkdir(mode=PRIVATE_DIR_MODE, parents=True, exist_ok=True)
    missing = []
    for name in names:
        _check_plain_name(name)
        data = store.read(name) if store is not None else None
        if data is None and mounted_dir is not None:
            data = _read_files(mounted_dir / name)
        if data is None:
            missing.append(name)
            continue
        _write_files(directory / name, data, MATERIALIZED_FILE_MODE)
    return missing


def remove_materialized(directory: Path) -> None:
    shutil.rmtree(directory, ignore_errors=True)


def _check_plain_name(name: str) -> None:
    """A Secret name or key must not climb out of its directory."""
    if not name or name in {".", ".."} or "/" in name or "\\" in name or name.startswith("."):
        raise ValueError(f"{name!r} is not a plain Secret name")


def _write_files(directory: Path, data: Mapping[str, str], mode: int) -> None:
    directory.mkdir(mode=PRIVATE_DIR_MODE, parents=True, exist_ok=True)
    for key, value in data.items():
        _check_plain_name(key)
        descriptor = os.open(directory / key, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, mode)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(value)


def _read_files(directory: Path) -> dict[str, str] | None:
    if not directory.is_dir():
        return None
    # A mounted Secret's keys are symlinks into ..data; following them is right.
    return {
        entry.name: entry.read_text(encoding="utf-8")
        for entry in sorted(directory.iterdir())
        if not entry.name.startswith(".") and entry.is_file()
    }
