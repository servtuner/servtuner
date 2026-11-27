"""What the Kubernetes runner needs of the cluster, and the lightkube client that does it."""

from collections.abc import Iterator
from typing import Protocol

from lightkube import Client
from lightkube.core.exceptions import ApiError
from lightkube.models.meta_v1 import OwnerReference
from lightkube.resources.batch_v1 import Job
from lightkube.resources.core_v1 import ConfigMap, Pod, Secret
from lightkube.types import CascadeType

from servtuner_api.kubernetes_decoders import prepare_decoders
from servtuner_api.kubernetes_runner.config import (
    BENCH_CONTAINER,
    FIELD_MANAGER,
    HTTP_NOT_FOUND,
    RUN_ID_LABEL,
)


class Cluster(Protocol):
    """The few Kubernetes calls the runner makes, so tests can stand in for the API server."""

    def secret_exists(self, name: str) -> bool: ...

    def create_job(self, job: Job) -> OwnerReference: ...

    def create_config_map(self, config_map: ConfigMap) -> None: ...

    def create_secret(self, secret: Secret) -> None: ...

    def get_job(self, name: str) -> Job | None: ...

    def delete_job(self, name: str) -> None: ...

    def list_pods(self, run_id: str) -> list[Pod]: ...

    def pod_log(self, pod_name: str) -> Iterator[str]: ...


class LightkubeCluster:
    """``Cluster`` over lightkube, in the runs namespace, with the pod's in-cluster config."""

    def __init__(self, namespace: str, client: Client | None = None) -> None:
        prepare_decoders()
        self._namespace = namespace
        self._client = client or Client(namespace=namespace, field_manager=FIELD_MANAGER)

    def secret_exists(self, name: str) -> bool:
        return self._get(Secret, name) is not None

    def create_job(self, job: Job) -> OwnerReference:
        created = self._client.create(job)
        assert created.metadata is not None and created.metadata.uid is not None
        assert created.metadata.name is not None
        return OwnerReference(
            apiVersion="batch/v1",
            kind="Job",
            name=created.metadata.name,
            uid=created.metadata.uid,
            blockOwnerDeletion=True,
            controller=False,
        )

    def create_config_map(self, config_map: ConfigMap) -> None:
        self._client.create(config_map)

    def create_secret(self, secret: Secret) -> None:
        self._client.create(secret)

    def get_job(self, name: str) -> Job | None:
        return self._get(Job, name)

    def delete_job(self, name: str) -> None:
        try:
            self._client.delete(Job, name, cascade=CascadeType.BACKGROUND)
        except ApiError as error:
            if error.status.code != HTTP_NOT_FOUND:
                raise

    def list_pods(self, run_id: str) -> list[Pod]:
        return list(self._client.list(Pod, labels={RUN_ID_LABEL: run_id.lower()}))

    def pod_log(self, pod_name: str) -> Iterator[str]:
        return self._client.log(pod_name, container=BENCH_CONTAINER, newlines=False)

    def _get[Resource: (Job, Secret)](self, kind: type[Resource], name: str) -> Resource | None:
        try:
            return self._client.get(kind, name)
        except ApiError as error:
            if error.status.code == HTTP_NOT_FOUND:
                return None
            raise
