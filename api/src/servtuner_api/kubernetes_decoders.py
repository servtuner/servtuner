"""lightkube's msgspec decoders, built once on the thread that starts the api.

lightkube decodes every API response with msgspec, which builds a type's decoder the first time it
meets the type. On Python 3.14 that build runs Python code (annotationlib resolves the lazily
evaluated annotations, PEP 649), so another thread can run while it is half done, and a second
thread decoding the same type for the first time crashed the api with a segmentation fault inside
msgspec._core: two runner threads polling their Jobs at once, right after a restart with two
unfinished runs (found by just e2e-airgap; msgspec 0.22.0, the latest, has no fix).
Building every decoder servtuner uses before any background thread starts takes the race away.
"""

import threading

import msgspec
from lightkube.core import decoders
from lightkube.models.meta_v1 import Status
from lightkube.resources.apps_v1 import Deployment
from lightkube.resources.batch_v1 import Job
from lightkube.resources.core_v1 import ConfigMap, PersistentVolumeClaim, Pod, Secret, Service

# Every typed resource the runner, the deployer and the secret store read, apply or list.
RESOURCES = (Job, Pod, ConfigMap, Secret, Deployment, Service, PersistentVolumeClaim)
# Resources servtuner lists: lightkube builds a list model for each on first use.
LISTED = (Pod,)

_lock = threading.Lock()
_prepared = False


def prepare_decoders() -> None:
    """Build lightkube's decoders for RESOURCES; idempotent. Call before starting threads."""
    global _prepared
    with _lock:
        if _prepared:
            return
        for resource in (*RESOURCES, Status):
            msgspec.json.Decoder(type=resource)
        for resource in LISTED:
            decoders.decode_list(b'{"items": []}', resource)
        _prepared = True
