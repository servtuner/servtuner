"""Bundle stores: how the api reads a run's bundle.

bench writes bundles; the api only reads. ``file`` reads the data directory,
``s3`` any S3-compatible store, both behind the same Protocol.
"""

import json
import shutil
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any, Protocol

import boto3
from botocore.config import Config
from botocore.exceptions import ClientError

from servtuner_api.database import RunRow
from servtuner_api.datadir import run_bundle_dir
from servtuner_api.errors import ForeignStoreError
from servtuner_api.settings.models import Connection, S3Settings, TlsSettings

MANIFEST_FILE = "manifest.json"
SUMMARY_FILE = "summary.json"


class BundlePathError(ValueError):
    """A requested path would leave the run's bundle."""


@dataclass(frozen=True)
class BundleFile:
    path: str
    size_bytes: int


# DEFAULT_STORE_NAME is the one store an api has today; runs record it so a
# second store, or a moved bucket, is a config change.
DEFAULT_STORE_NAME = "default"


class BundleStore(Protocol):
    """Bundles are addressed by key: the store's own path to a run's bundle,
    which Postgres keeps beside the store name."""

    @property
    def name(self) -> str: ...

    def key(self, run_id: str) -> str:
        """The key a new run's bundle is written under."""
        ...

    def uri(self, key: str) -> str: ...

    def get(self, key: str, path: str) -> bytes | None:
        """The file's bytes, or None when the bundle has no such file."""
        ...

    def list_files(self, key: str) -> list[BundleFile]: ...

    def stream(self, key: str, path: str) -> Iterator[bytes] | None:
        """The file in chunks, or None when the bundle has no such file."""
        ...

    def presign(self, key: str, path: str) -> str | None:
        """A time-limited download URL, or None to have the api proxy the file."""
        ...

    def delete(self, key: str) -> None:
        """Remove every file of the bundle (a user's delete); absent is not an error,
        so a delete that stopped half way finishes when retried."""
        ...


# STREAM_CHUNK_BYTES is the size of each chunk a proxied download is sent in.
STREAM_CHUNK_BYTES = 1_048_576  # 1 MiB
# PRESIGN_SECONDS is how long a presigned download link works.
PRESIGN_SECONDS = 300


class FileBundleStore:
    name = DEFAULT_STORE_NAME

    def __init__(self, data_dir: Path) -> None:
        self._data_dir = data_dir

    def key(self, run_id: str) -> str:
        return run_bundle_dir(PurePosixPath(), run_id).as_posix() + "/"

    def uri(self, key: str) -> str:
        return self._bundle_dir(key).absolute().as_uri() + "/"

    def get(self, key: str, path: str) -> bytes | None:
        file_path = self._resolve(key, path)
        return file_path.read_bytes() if file_path.is_file() else None

    def list_files(self, key: str) -> list[BundleFile]:
        bundle_dir = self._bundle_dir(key)
        if not bundle_dir.is_dir():
            return []
        return sorted(
            (
                BundleFile(str(file.relative_to(bundle_dir)), file.stat().st_size)
                for file in bundle_dir.rglob("*")
                if file.is_file()
            ),
            key=lambda bundle_file: bundle_file.path,
        )

    def stream(self, key: str, path: str) -> Iterator[bytes] | None:
        file_path = self._resolve(key, path)
        if not file_path.is_file():
            return None
        handle = file_path.open("rb")

        def chunks() -> Iterator[bytes]:
            with handle:
                while chunk := handle.read(STREAM_CHUNK_BYTES):
                    yield chunk

        return chunks()

    def presign(self, key: str, path: str) -> str | None:
        self._resolve(key, path)  # refuse a path outside the bundle all the same
        return None

    def delete(self, key: str) -> None:
        bundle_dir = self._bundle_dir(key)
        if bundle_dir.is_dir():
            shutil.rmtree(bundle_dir)

    def _bundle_dir(self, key: str) -> Path:
        bundle_dir = (self._data_dir / key).resolve()
        if not bundle_dir.is_relative_to(self._data_dir.resolve()):
            raise BundlePathError(f"key {key!r} is outside the data directory")
        return bundle_dir

    def _resolve(self, key: str, path: str) -> Path:
        bundle_dir = self._bundle_dir(key)
        resolved = (bundle_dir / path).resolve()
        if not resolved.is_relative_to(bundle_dir):
            raise BundlePathError(f"path {path!r} is outside the bundle {key}")
        return resolved


class S3BundleStore:
    """Reads bundles that bench uploaded to s3://<bucket>/<prefix><run id>/."""

    def __init__(
        self,
        settings: S3Settings,
        tls: TlsSettings,
        client: Any = None,
    ) -> None:
        self._settings = settings
        self._client: Any = client or _s3_client(settings, tls)

    name = DEFAULT_STORE_NAME

    def key(self, run_id: str) -> str:
        return f"{self._settings.prefix}{run_id}/"

    def uri(self, key: str) -> str:
        return f"s3://{self._settings.bucket}/{key}"

    def get(self, key: str, path: str) -> bytes | None:
        object_key = self._object_key(key, path)
        try:
            response = self._client.get_object(Bucket=self._settings.bucket, Key=object_key)
        except ClientError as error:
            if _is_missing(error):
                return None
            raise
        content: bytes = response["Body"].read()
        return content

    def list_files(self, key: str) -> list[BundleFile]:
        paginator = self._client.get_paginator("list_objects_v2")
        files: list[BundleFile] = []
        for page in paginator.paginate(Bucket=self._settings.bucket, Prefix=key):
            for item in page.get("Contents", []):
                files.append(BundleFile(str(item["Key"])[len(key) :], int(item["Size"])))
        return sorted(files, key=lambda bundle_file: bundle_file.path)

    def stream(self, key: str, path: str) -> Iterator[bytes] | None:
        object_key = self._object_key(key, path)
        try:
            response = self._client.get_object(Bucket=self._settings.bucket, Key=object_key)
        except ClientError as error:
            if _is_missing(error):
                return None
            raise
        chunks: Iterator[bytes] = response["Body"].iter_chunks(STREAM_CHUNK_BYTES)
        return chunks

    def presign(self, key: str, path: str) -> str | None:
        object_key = self._object_key(key, path)
        if not self._settings.presign:
            return None
        url: str = self._client.generate_presigned_url(
            "get_object",
            Params={"Bucket": self._settings.bucket, "Key": object_key},
            ExpiresIn=PRESIGN_SECONDS,
        )
        return url

    def delete(self, key: str) -> None:
        paginator = self._client.get_paginator("list_objects_v2")
        for page in paginator.paginate(Bucket=self._settings.bucket, Prefix=key):
            objects = [{"Key": item["Key"]} for item in page.get("Contents", [])]
            # A listing page holds at most 1000 keys, delete_objects' own limit.
            if objects:
                self._client.delete_objects(
                    Bucket=self._settings.bucket, Delete={"Objects": objects, "Quiet": True}
                )

    def _object_key(self, key: str, path: str) -> str:
        relative = PurePosixPath(path)
        if relative.is_absolute() or ".." in relative.parts or not relative.parts:
            raise BundlePathError(f"path {path!r} is outside the bundle {key}")
        return f"{key}{relative}"


def _is_missing(error: ClientError) -> bool:
    code = error.response.get("Error", {}).get("Code", "")
    return code in {"NoSuchKey", "404", "NotFound"}


def _s3_client(settings: S3Settings, tls: TlsSettings) -> Any:
    """A boto3 client for the store, with the object-store connection's TLS."""
    options: dict[str, Any] = {
        "s3": {"addressing_style": "path" if settings.force_path_style else "auto"},
        # SigV4 for presigned URLs too: boto3 presigns with legacy SigV2 otherwise.
        "signature_version": "s3v4",
        "request_checksum_calculation": settings.checksum_mode.value,
        "response_checksum_validation": settings.checksum_mode.value,
    }
    verify: str | bool = True
    if tls.uses_tls(Connection.OBJECT_STORE) and settings.endpoint is not None:
        verify = str(tls.ca_bundle_file) if tls.ca_bundle_file else True
        if tls.presents_cert(Connection.OBJECT_STORE):
            assert tls.cert_file is not None and tls.key_file is not None  # Settings require them
            options["client_cert"] = (str(tls.cert_file), str(tls.key_file))
    # Static keys from the chart's Secret with source secret; None leaves the
    # SDK's default chain (Pod Identity, IRSA, an instance role) to find them.
    session = boto3.session.Session(
        aws_access_key_id=settings.access_key_id,
        aws_secret_access_key=settings.secret_access_key,
        region_name=settings.region,
    )
    # Fail at startup, never later or anonymously.
    if session.get_credentials() is None:
        raise MissingCredentialsError(
            "S3 bundle store: no AWS credentials resolved. With credentials source ambient, "
            "check the EKS Pod Identity Agent and association, IRSA, or the instance role; "
            "with source secret, check the Secret's keys."
        )
    return session.client(
        "s3", endpoint_url=settings.endpoint, config=Config(**options), verify=verify
    )


class MissingCredentialsError(RuntimeError):
    """The S3 store has no credentials to sign with."""


def read_bundle_json(store: BundleStore, key: str, path: str) -> object | None:
    content = store.get(key, path)
    return None if content is None else json.loads(content)


def bundle_key(store: BundleStore, run: RunRow) -> str:
    """A run's key in the store, or ForeignStoreError when the run was written to another."""
    if run.bundle_store != store.name:
        raise ForeignStoreError(
            f"run {run.id} was written to store {run.bundle_store!r}, "
            f"which this api does not have (it has {store.name!r})"
        )
    return run.bundle_key
