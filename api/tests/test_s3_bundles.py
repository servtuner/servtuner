"""S3BundleStore against real S3-compatible stores in containers.

The images match bench's conformance tests (bench/internal/storage/s3test).
"""

import json
import time
import urllib.request
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import boto3
import pytest
from botocore.config import Config
from testcontainers.core.container import DockerContainer

from servtuner_api.bundles import BundlePathError, S3BundleStore, read_bundle_json
from servtuner_api.settings.models import (
    ChecksumMode,
    CredentialsSource,
    S3Settings,
    TlsSettings,
    TransportMode,
)

ACCESS_KEY = "servtunertestaccess"
SECRET_KEY = "servtunertestsecret0123"
BUCKET = "servtuner-runs"
RUN_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
SEAWEED_IDENTITIES = {
    "identities": [
        {
            "name": "servtuner",
            "credentials": [{"accessKey": ACCESS_KEY, "secretKey": SECRET_KEY}],
            "actions": ["Admin", "Read", "Write", "List", "Tagging"],
        }
    ]
}
PLAIN_TLS = TlsSettings(
    hops={}, cert_file=None, key_file=None, ca_bundle_file=None, client_ca_file=None
)


def rustfs() -> DockerContainer:
    return (
        DockerContainer("rustfs/rustfs:1.0.1")
        .with_env("RUSTFS_ACCESS_KEY", ACCESS_KEY)
        .with_env("RUSTFS_SECRET_KEY", SECRET_KEY)
        .with_exposed_ports(9000)
        .with_command("/data")
    )


def seaweedfs(config_dir: Path) -> DockerContainer:
    # The image drops to its seaweed user (uid 1000), not this test's user on every machine: the
    # config (test keys only) must be readable by anyone, unlike a 0700 temporary directory.
    config = config_dir / "s3.json"
    config.write_text(json.dumps(SEAWEED_IDENTITIES))
    config_dir.chmod(0o755)
    config.chmod(0o644)
    return (
        DockerContainer("chrislusf/seaweedfs:4.48")
        .with_volume_mapping(str(config_dir), "/etc/servtuner", "ro")
        .with_exposed_ports(8333)
        .with_command("server -s3 -s3.config=/etc/servtuner/s3.json -dir=/data")
    )


def admin_client(endpoint: str) -> Any:
    return boto3.client(
        "s3",
        region_name="us-east-1",
        endpoint_url=endpoint,
        aws_access_key_id=ACCESS_KEY,
        aws_secret_access_key=SECRET_KEY,
        config=Config(s3={"addressing_style": "path"}),
    )


def wait_for_bucket(client: Any, container: DockerContainer) -> None:
    deadline = time.monotonic() + 60
    while True:
        try:
            client.create_bucket(Bucket=BUCKET)
            return
        except client.exceptions.BucketAlreadyOwnedByYou:
            return
        except Exception as error:  # the store is still starting
            if time.monotonic() > deadline:
                stdout, stderr = container.get_logs()
                logs = (stdout + stderr).decode(errors="replace")[-4000:]
                raise AssertionError(
                    f"the S3 store never answered; its log ends:\n{logs}"
                ) from error
            time.sleep(1)


@pytest.fixture(scope="module", params=["rustfs", "seaweedfs"])
def endpoint(
    request: pytest.FixtureRequest, docker_required: None, tmp_path_factory: pytest.TempPathFactory
) -> Iterator[str]:
    if request.param == "rustfs":
        container, port = rustfs(), 9000
    else:
        container, port = seaweedfs(tmp_path_factory.mktemp("seaweed")), 8333
    with container:
        host = container.get_container_host_ip()
        url = f"http://{host}:{container.get_exposed_port(port)}"
        wait_for_bucket(admin_client(url), container)
        yield url


def store_for(endpoint: str, *, presign: bool = False) -> S3BundleStore:
    settings = S3Settings(
        endpoint=endpoint,
        region="us-east-1",
        bucket=BUCKET,
        prefix="runs/",
        force_path_style=True,
        checksum_mode=ChecksumMode.WHEN_REQUIRED,
        credentials_source=CredentialsSource.SECRET,
        presign=presign,
        access_key_id=ACCESS_KEY,
        secret_access_key=SECRET_KEY,
    )
    return S3BundleStore(settings, TransportMode.PLATFORM, PLAIN_TLS)


def test_reads_what_bench_uploaded(endpoint: str) -> None:
    admin = admin_client(endpoint)
    for path, body in {
        "manifest.json": b'{"status": "succeeded"}',
        "prometheus/vllm/kv.json": b"{}",
    }.items():
        admin.put_object(Bucket=BUCKET, Key=f"runs/{RUN_ID}/{path}", Body=body)
    store = store_for(endpoint)
    key = store.key(RUN_ID)
    assert key == f"runs/{RUN_ID}/"
    assert store.uri(key) == f"s3://{BUCKET}/runs/{RUN_ID}/"
    assert read_bundle_json(store, key, "manifest.json") == {"status": "succeeded"}
    assert [(f.path, f.size_bytes) for f in store.list_files(key)] == [
        ("manifest.json", 23),
        ("prometheus/vllm/kv.json", 2),
    ]
    assert store.get(key, "summary.json") is None
    assert store.list_files(store.key("01ARZ3NDEKTSV4RRFFQ69G5FAA")) == []


def test_paths_cannot_leave_the_bundle(endpoint: str) -> None:
    store = store_for(endpoint)
    for path in ("../other/manifest.json", "/etc/passwd", ""):
        with pytest.raises(BundlePathError):
            store.get(store.key(RUN_ID), path)


def test_files_stream_or_presign(endpoint: str) -> None:
    """The api proxies by default, and hands out presigned GETs on request."""
    large = b"servtuner " * (1 << 19)  # 3.5 MiB: several stream chunks
    admin_client(endpoint).put_object(
        Bucket=BUCKET, Key=f"runs/{RUN_ID}/requests.parquet", Body=large
    )
    store = store_for(endpoint)
    key = store.key(RUN_ID)
    assert store.presign(key, "requests.parquet") is None
    chunks = store.stream(key, "requests.parquet")
    assert chunks is not None and b"".join(chunks) == large
    assert store.stream(key, "missing.json") is None
    url = store_for(endpoint, presign=True).presign(key, "requests.parquet")
    assert url is not None and "X-Amz-Signature=" in url
    with urllib.request.urlopen(url) as response:  # noqa: S310 - the test store's own URL
        assert response.read() == large
    with pytest.raises(BundlePathError):
        store_for(endpoint, presign=True).presign(key, "../other/x.json")


def test_a_deleted_bundle_leaves_its_neighbours(endpoint: str) -> None:
    admin = admin_client(endpoint)
    doomed, neighbour = "01ARZ3NDEKTSV4RRFFQ69G5FD1", "01ARZ3NDEKTSV4RRFFQ69G5FD2"
    for run_id in (doomed, neighbour):
        for path in ("manifest.json", "raw/requests.parquet"):
            admin.put_object(Bucket=BUCKET, Key=f"runs/{run_id}/{path}", Body=b"{}")
    store = store_for(endpoint)
    store.delete(store.key(doomed))
    assert store.list_files(store.key(doomed)) == []
    assert len(store.list_files(store.key(neighbour))) == 2
    store.delete(store.key(doomed))  # already gone: a retried delete finishes quietly
