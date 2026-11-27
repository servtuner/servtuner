"""lightkube's decoders are built before any thread decodes (servtuner_api.kubernetes_decoders)."""

import threading

from lightkube.core import decoders
from lightkube.resources.batch_v1 import Job

from servtuner_api import kubernetes_decoders
from servtuner_api.kubernetes_decoders import RESOURCES, prepare_decoders
from servtuner_api.kubernetes_runner.cluster import LightkubeCluster
from servtuner_api.secret_store import KubernetesSecretStore

JOB = b'{"apiVersion": "batch/v1", "kind": "Job", "metadata": {"name": "bench-x"}}'


def test_every_wrapper_prepares_the_decoders_first(monkeypatch) -> None:  # type: ignore[no-untyped-def]
    calls: list[str] = []
    monkeypatch.setattr(kubernetes_decoders, "_prepared", False)
    monkeypatch.setattr(
        "servtuner_api.kubernetes_runner.cluster.prepare_decoders", lambda: calls.append("runner")
    )
    monkeypatch.setattr(
        "servtuner_api.secret_store.prepare_decoders", lambda: calls.append("secrets")
    )
    LightkubeCluster("servtuner", client=object())  # type: ignore[arg-type]
    KubernetesSecretStore("servtuner", client=object())  # type: ignore[arg-type]
    assert calls == ["runner", "secrets"]


def test_prepared_decoders_decode_from_many_threads_at_once() -> None:
    prepare_decoders()
    prepare_decoders()  # idempotent
    assert Job in RESOURCES
    results: list[str] = []

    def decode() -> None:
        results.append(decoders.decode_object(JOB, Job).metadata.name)

    threads = [threading.Thread(target=decode) for _ in range(16)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    assert results == ["bench-x"] * 16
