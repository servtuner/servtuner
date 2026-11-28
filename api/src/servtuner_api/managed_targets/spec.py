"""The managed target template: what is stored, and the checks it passes."""

from __future__ import annotations

import re
from collections.abc import Mapping
from typing import Annotated, Any, Literal

from pydantic import (
    BaseModel,
    ConfigDict,
    Discriminator,
    Field,
    Tag,
    TypeAdapter,
    field_validator,
    model_validator,
)

# A DNS label: Kubernetes object names, namespaces and the Service host.
DNS_LABEL = r"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$"


# A Kubernetes quantity such as 8, 500m, 32Gi or 200G.
QUANTITY = r"^[0-9]+(\.[0-9]+)?(m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei)?$"


# Kubernetes quantity suffixes as multipliers: decimal (k = 1000), binary (Ki = 1024), milli.
QUANTITY_FACTORS = {
    "m": 1e-3,
    "": 1,
    **{suffix: 1000**power for power, suffix in enumerate("kMGTPE", start=1)},
    **{f"{suffix}i": 1024**power for power, suffix in enumerate("KMGTPE", start=1)},
}


def quantity_value(quantity: str) -> float:
    """A Kubernetes quantity (matching QUANTITY) as a plain number: "16Gi" is 16 * 1024**3."""
    number = re.match(r"^[0-9]+(\.[0-9]+)?", quantity)
    assert number is not None
    return float(number.group(0)) * QUANTITY_FACTORS[quantity[number.end() :]]


# An image with a tag or a digest. "latest" is refused separately: runs must
# record what actually ran, and a moving tag cannot say.
IMAGE = (
    r"^[a-z0-9][a-z0-9._/:-]*[a-z0-9](:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$"
)


# The port vLLM serves on in every managed Deployment.
SERVING_PORT = 8000


# The label that ties every object of a managed target together (and nothing else).
MANAGED_TARGET_LABEL = "servtuner.dev/managed-target"


# The run label naming the managed target a run measured (its id, lower case like the session
# label): New run's Benchmark and tuning sessions set it, and the target's page lists by it.
MANAGED_TARGET_RUN_LABEL = "servtuner.managed-target"


MAX_DESCRIPTION = 500


MAX_GPUS = 8  # one node: multi-node serving is out of scope


# An environment variable name, as POSIX shells spell one.
ENV_NAME = r"^[A-Za-z_][A-Za-z0-9_]*$"


# Variables the deployer sets itself (the model cache and the token Secret).
SERVTUNER_OWNED_ENV = frozenset(
    {
        "HF_HOME",
        "HF_TOKEN",
        "HF_ENDPOINT",
        "HF_HUB_OFFLINE",
        "AWS_ENDPOINT_URL",
        "AWS_ACCESS_KEY_ID",
        "AWS_SECRET_ACCESS_KEY",
        "AWS_EC2_METADATA_DISABLED",
        "RUNAI_STREAMER_S3_USE_VIRTUAL_ADDRESSING",
    }
)


# Flags the deployer sets itself, so a base flag may not: the model and where it
# is served, the cache path, and the transport. bench's servtunerOwnedFields
# (target/catalog.go) keeps the same flags from being knobs.
SERVTUNER_OWNED_FLAGS = frozenset(
    {
        "--model",
        "--served-model-name",
        "--host",
        "--port",
        "--download-dir",
        "--ssl-keyfile",
        "--ssl-certfile",
        "--ssl-ca-certs",
        "--ssl-cert-reqs",
        "--enable-ssl-refresh",
    }
)


class SecretKeyRef(BaseModel):
    """A key of a Secret in the managed namespace, by name only (root rule 4)."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(pattern=DNS_LABEL)
    key: str = Field(min_length=1, max_length=253)


class Toleration(BaseModel):
    """A pod toleration, as Kubernetes spells it."""

    model_config = ConfigDict(extra="forbid")

    key: str | None = None
    operator: Literal["Exists", "Equal"] = "Equal"
    value: str | None = None
    effect: Literal["NoSchedule", "PreferNoSchedule", "NoExecute"] | None = None


class ModelCache(BaseModel):
    """The PVC that keeps the weights across restarts (HF_HOME), so a restart downloads nothing."""

    model_config = ConfigDict(extra="forbid")

    size: str = Field(pattern=QUANTITY)
    storage_class: str | None = Field(default=None, pattern=DNS_LABEL)


class HubModel(BaseModel):
    """Download the weights from Hugging Face into the model cache (the default)."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["hub"] = "hub"


class MirrorModel(BaseModel):
    """Download them from an internal Hugging Face mirror instead (HF_ENDPOINT)."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["mirror"]
    endpoint: str = Field(pattern=r"^https?://\S+$", max_length=512)


class PvcModel(BaseModel):
    """Read them from a claim the operator seeded with a model archive, and never download
    (HF_HUB_OFFLINE=1). servtuner never deletes the claim."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["pvc"]
    claim: str = Field(pattern=DNS_LABEL)


class ObjectStoreCredentials(BaseModel):
    """A Secret holding the store's access keys, by key name; never a value."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(pattern=DNS_LABEL)
    access_key_id_key: str = Field(default="access_key_id", min_length=1, max_length=253)
    secret_access_key_key: str = Field(default="secret_access_key", min_length=1, max_length=253)


class ObjectStoreModel(BaseModel):
    """Stream the weights from an object store someone else filled: vLLM's Run:ai
    Model Streamer reads safetensors from s3://, gs:// or az:// straight into GPU memory
    (--load-format runai_streamer, in the stock vllm-openai image). Without credentials vLLM uses
    the pod's own identity (EKS Pod Identity, IRSA, GKE Workload Identity)."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["object_store"]
    uri: str = Field(pattern=r"^(s3|gs|az)://[^/\s]+(/\S*)?$", max_length=1024)
    # An S3-compatible store (RustFS, Ceph RGW, MinIO, …): path-style addressing, no EC2 metadata.
    endpoint: str | None = Field(default=None, pattern=r"^https?://\S+$", max_length=512)
    credentials: ObjectStoreCredentials | None = None

    @model_validator(mode="after")
    def _s3_options(self) -> ObjectStoreModel:
        if not self.uri.startswith("s3://") and (self.endpoint or self.credentials):
            raise ValueError(
                "endpoint and credentials apply to s3:// only; gs:// and az:// use pod identity"
            )
        return self


ModelSource = Annotated[
    HubModel | MirrorModel | PvcModel | ObjectStoreModel, Field(discriminator="kind")
]


class IssuerRef(BaseModel):
    """The cert-manager issuer of the target's own certificate."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["Issuer", "ClusterIssuer"]
    name: str = Field(min_length=1, max_length=253)
    group: str = "cert-manager.io"


class PlainTransport(BaseModel):
    """Plain HTTP (the default): no TLS from vLLM. Use it where a mesh or an encrypted network
    encrypts the hop, or for local testing."""

    model_config = ConfigDict(extra="forbid")

    enabled: Literal[False] = False


class TlsTransport(BaseModel):
    """vLLM serves TLS with the target's own certificate: from a cert-manager issuer, or a
    kubernetes.io/tls Secret only this target uses. Client certificates are not offered yet
    (client_auth is none), so the shape can take them later without changing."""

    model_config = ConfigDict(extra="forbid")

    enabled: Literal[True]
    client_auth: Literal["none"] = "none"
    issuer: IssuerRef | None = None
    secret: str | None = Field(default=None, pattern=DNS_LABEL)

    @model_validator(mode="after")
    def _one_source(self) -> TlsTransport:
        if (self.issuer is None) == (self.secret is None):
            raise ValueError("tls needs exactly one certificate source: issuer or secret")
        return self


# A plain union, not a discriminated one: OpenAPI discriminators are strings, so a boolean one
# would reach the frontend's types as "true" and "false". The Literal enabled fields and
# extra="forbid" already make the two unambiguous.
Transport = PlainTransport | TlsTransport


def checked_env(env: dict[str, str]) -> dict[str, str]:
    """vLLM's environment: variable names only, and none servtuner sets itself."""
    bad = sorted(name for name in env if not re.match(ENV_NAME, name))
    if bad:
        raise ValueError(f"not environment variable names: {', '.join(bad)}")
    owned = sorted(set(env) & SERVTUNER_OWNED_ENV)
    if owned:
        raise ValueError(
            f"servtuner sets {', '.join(owned)} itself (the token comes from hf_token)"
        )
    return env


# The flags the deployer sets on an SGLang server (manifests.serving_args), kept out of its base
# flags for the same reason.
SGLANG_OWNED_FLAGS = frozenset(
    {
        "--model-path",
        "--model",
        "--served-model-name",
        "--host",
        "--port",
        "--download-dir",
        "--enable-metrics",
        "--ssl-keyfile",
        "--ssl-certfile",
        "--ssl-ca-certs",
        "--enable-ssl-refresh",
    }
)

# The model servers a managed target can run.
ModelServer = Literal["vllm", "sglang"]


def checked_sglang_flags(flags: list[str]) -> None:
    """An SGLang target's base flags: none of the flags servtuner sets on SGLang."""
    owned = sorted({flag.split("=", 1)[0] for flag in flags} & SGLANG_OWNED_FLAGS)
    if owned:
        raise ValueError(f"servtuner sets {', '.join(owned)} itself")


def checked_base_flags(flags: list[str]) -> list[str]:
    """Flags never tuned, and none the deployer sets itself."""
    owned = sorted({flag.split("=", 1)[0] for flag in flags} & SERVTUNER_OWNED_FLAGS)
    if owned:
        raise ValueError(f"servtuner sets {', '.join(owned)} itself")
    return flags


class ManagedTargetSpec(BaseModel):
    """A model server (vLLM, or SGLang) on Kubernetes: the image, model and resources the
    deployer creates, and the flags never tuned."""

    model_config = ConfigDict(extra="forbid")

    placement: Literal["kubernetes"] = "kubernetes"
    # Which server the image runs; targets stored before SGLang support are vLLM.
    server: ModelServer = "vllm"
    image: str = Field(pattern=IMAGE, max_length=512)
    model: str = Field(min_length=1, max_length=256)
    served_model_name: str | None = Field(default=None, min_length=1, max_length=256)
    # 0 requests no GPU: a vLLM on its CPU backend, or the mock.
    gpus: int = Field(ge=0, le=MAX_GPUS)
    cpu: str = Field(pattern=QUANTITY)
    memory: str = Field(pattern=QUANTITY)
    node_selector: dict[str, str] = Field(default_factory=dict)
    tolerations: list[Toleration] = Field(default_factory=list)
    hf_token: SecretKeyRef | None = None
    base_flags: list[str] = Field(default_factory=list)
    # Plain environment variables for vLLM (VLLM_* settings and the like); never a secret: the
    # Hugging Face token comes from hf_token, a Secret reference.
    env: dict[str, str] = Field(default_factory=dict)
    # Where the weights live: the cache servtuner creates (hub, mirror), or a seeded claim (pvc).
    model_cache: ModelCache | None = None
    model_source: ModelSource = Field(default_factory=HubModel)
    transport: Transport = Field(default_factory=PlainTransport)
    # /dev/shm, a memory-backed volume: tensor parallel workers (NCCL) and large models exchange
    # data through it. Unset, it is unbounded (up to the memory limit, which it counts against).
    shm_size: str | None = Field(default=None, pattern=QUANTITY)

    @model_validator(mode="after")
    def _one_model_cache(self) -> ManagedTargetSpec:
        downloads = isinstance(self.model_source, HubModel | MirrorModel)
        if not downloads and self.model_cache is not None:
            raise ValueError(
                f"a {self.model_source.kind} model source keeps no download cache: drop model_cache"
            )
        if downloads and self.model_cache is None:
            raise ValueError("model_cache is required: the weights are downloaded into it")
        if isinstance(self.model_source, ObjectStoreModel) and any(
            flag.split("=", 1)[0] == "--load-format" for flag in self.base_flags
        ):
            raise ValueError("an object_store model source sets --load-format itself")
        return self

    @model_validator(mode="after")
    def _sglang_flags(self) -> ManagedTargetSpec:
        if self.server != "sglang":
            return self
        if isinstance(self.model_source, ObjectStoreModel):
            raise ValueError(
                "an SGLang target cannot stream from an object store: use hub, mirror or pvc"
            )
        checked_sglang_flags(self.base_flags)
        return self

    @model_validator(mode="after")
    def _shm_within_memory(self) -> ManagedTargetSpec:
        # Kubernetes charges a memory-backed volume to the pod's memory limit: a /dev/shm as big
        # as the memory would leave vLLM nothing and get it OOM-killed.
        if self.shm_size and quantity_value(self.shm_size) >= quantity_value(self.memory):
            raise ValueError(
                f"shm_size {self.shm_size} counts against memory {self.memory}: make it smaller"
            )
        return self

    @field_validator("image")
    @classmethod
    def _pinned(cls, image: str) -> str:
        has_digest = "@sha256:" in image
        tag = image.split("@")[0].rsplit("/", 1)[-1].partition(":")[2]
        if not tag and not has_digest:
            raise ValueError("pin the image with a tag or a digest")
        if tag == "latest" and not has_digest:
            raise ValueError("the latest tag moves; pin a version or a digest")
        return image

    @field_validator("env")
    @classmethod
    def _env(cls, env: dict[str, str]) -> dict[str, str]:
        return checked_env(env)

    @field_validator("base_flags")
    @classmethod
    def _base_flags(cls, flags: list[str]) -> list[str]:
        return checked_base_flags(flags)


class TargetMetricsRef(BaseModel):
    """How the host's vLLM is labelled in Prometheus: its scrape job and the pod label it gives the
    series (namespace is the target's own), plus its GPU exporter's host label, if any."""

    model_config = ConfigDict(extra="forbid")

    job: str = Field(min_length=1, max_length=253)
    pod: str = Field(pattern=DNS_LABEL)
    node: str | None = Field(default=None, min_length=1, max_length=253)


class HostTargetSpec(BaseModel):
    """A model server (vLLM, or SGLang) on a host outside Kubernetes, run by ``servtuner
    launcher`` there. The launcher's token and the server's API key are Secrets in the runs
    namespace, by name only."""

    model_config = ConfigDict(extra="forbid")

    placement: Literal["host"]
    # Which server the launcher starts; targets stored before hosts could run SGLang are vLLM.
    server: ModelServer = "vllm"
    # The host record this server runs on: its hardware and price. Optional: targets
    # saved before hosts existed have none.
    host_id: str | None = Field(default=None, min_length=26, max_length=26)
    launcher_url: str = Field(pattern=r"^https?://[^/\s]+/?$", max_length=512)
    # Set with the target, or afterwards through PUT .../credentials/launcher_token (a pasted
    # token servtuner keeps in a Secret it owns); deploys need it.
    launcher_token: SecretKeyRef | None = None
    # The vLLM base URL clients reach (through any port proxy), and the port vLLM binds on the host.
    endpoint: str = Field(pattern=r"^http://[^\s]+/v1$", max_length=512)
    port: int = Field(default=SERVING_PORT, ge=1, le=65535)
    model: str = Field(min_length=1, max_length=256)
    served_model_name: str | None = Field(default=None, min_length=1, max_length=256)
    base_flags: list[str] = Field(default_factory=list)
    env: dict[str, str] = Field(default_factory=dict)
    auth: SecretKeyRef | None = None
    metrics: TargetMetricsRef

    @field_validator("env")
    @classmethod
    def _env(cls, env: dict[str, str]) -> dict[str, str]:
        return checked_env(env)

    @field_validator("base_flags")
    @classmethod
    def _base_flags(cls, flags: list[str]) -> list[str]:
        return checked_base_flags(flags)

    @model_validator(mode="after")
    def _sglang_flags(self) -> HostTargetSpec:
        if self.server == "sglang":
            checked_sglang_flags(self.base_flags)
        return self


def _placement(value: Any) -> str:
    """Targets stored before hosts existed have no placement: they are Kubernetes targets."""
    if isinstance(value, dict):
        return str(value.get("placement", "kubernetes"))
    return str(getattr(value, "placement", "kubernetes"))


AnyTargetSpec = Annotated[
    Annotated[ManagedTargetSpec, Tag("kubernetes")] | Annotated[HostTargetSpec, Tag("host")],
    Discriminator(_placement),
]


SPEC_ADAPTER: TypeAdapter[ManagedTargetSpec | HostTargetSpec] = TypeAdapter(AnyTargetSpec)


def parse_spec(stored: Mapping[str, Any]) -> ManagedTargetSpec | HostTargetSpec:
    return SPEC_ADAPTER.validate_python(stored)
