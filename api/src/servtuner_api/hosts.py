"""Hosts: a machine servtuner runs vLLM on, recorded once.

A host holds what the machine is (GPUs, CPU, memory: typed, or read from its bench
launcher's GET /machine) and what it costs per hour. Host targets name their host
(``host_id``), so one price and one hardware description serve every target on the
machine. A host in use by a target cannot be deleted.
"""

from collections.abc import Callable
from datetime import UTC, datetime
from typing import Annotated, Literal

from pydantic import BaseModel, ConfigDict, Field

from servtuner_api.database import HostRow
from servtuner_api.ids import new_ulid
from servtuner_api.models import HostMachine
from servtuner_api.models.generated.host_machine_schema import HostGpu
from servtuner_api.records.repository import RunRepository
from servtuner_api.targets.common import (
    TargetConflictError,
    TargetInputError,
    TargetNotFoundError,
    parse_body,
)

DNS_LABEL = r"^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$"
MAX_NOTE = 500
WATTS_PER_KILOWATT = 1000


class HostHardware(BaseModel):
    """What the machine is. ``source`` says whether a person typed it or its launcher read it."""

    model_config = ConfigDict(extra="forbid")

    gpus: list[HostGpu] = Field(default_factory=list)
    cpu_model: str | None = Field(default=None, min_length=1, max_length=256)
    cpu_cores: int | None = Field(default=None, ge=1)
    memory_bytes: int | None = Field(default=None, ge=1)
    source: Literal["manual", "launcher"] = "manual"
    read_at: datetime | None = None

    @classmethod
    def of(cls, machine: HostMachine) -> HostHardware:
        return cls(
            gpus=machine.gpus,
            cpu_model=machine.cpu_model,
            cpu_cores=machine.cpu_cores,
            memory_bytes=machine.memory_bytes,
            source="launcher",
            read_at=machine.read_at.root,
        )


class GivenPrice(BaseModel):
    """The machine's price per hour as given (a rental, or a figure someone worked out)."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["given"]
    usd_per_hour: float = Field(gt=0)


class OwnedPrice(BaseModel):
    """Owned hardware: purchase ÷ amortization hours + watts ÷ 1000 × electricity, the same
    formula as bench's static GPU price (bench/internal/pricing gpuPrice), for the whole machine."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["owned"]
    purchase_usd: float = Field(gt=0)
    amortization_hours: float = Field(gt=0)
    watts: float = Field(ge=0)
    usd_per_kwh: float = Field(ge=0)


HostPrice = Annotated[GivenPrice | OwnedPrice, Field(discriminator="kind")]


def hourly(price: GivenPrice | OwnedPrice) -> tuple[float, str]:
    """The price per machine-hour and how it was worked out, for people."""
    if isinstance(price, GivenPrice):
        return price.usd_per_hour, "given"
    per_hour = (
        price.purchase_usd / price.amortization_hours
        + price.watts / WATTS_PER_KILOWATT * price.usd_per_kwh
    )
    basis = (
        f"{price.purchase_usd:g} USD ÷ {price.amortization_hours:g} h + {price.watts:g} W ÷ 1000 × "
        f"{price.usd_per_kwh:g} USD/kWh = {per_hour:.4g} USD/h"
    )
    return per_hour, basis


def declared_hardware(row: HostRow) -> dict[str, object] | None:
    """The RunSpec target.hardware of a run on this host, or None while its hardware
    is unknown. One GPU model is declared: the first, since a vLLM uses GPUs of one model."""
    hardware = HostHardware.model_validate(row.hardware)
    if not hardware.gpus and hardware.cpu_cores is None:
        return None
    declared: dict[str, object] = {
        "host": row.name,
        "accelerator": hardware.gpus[0].model if hardware.gpus else "none",
        "gpu_count": len(hardware.gpus),
    }
    if hardware.gpus:
        declared["gpu_memory_bytes"] = hardware.gpus[0].memory_bytes
        if hardware.gpus[0].driver_version:
            declared["driver_version"] = hardware.gpus[0].driver_version
    if hardware.cpu_model:
        declared["cpu_model"] = hardware.cpu_model
    if hardware.cpu_cores:
        declared["cpu_cores"] = hardware.cpu_cores
    return declared


def infra_pricing(row: HostRow) -> dict[str, object] | None:
    """The RunSpec analysis.infra_pricing of a run on this host: a static price for its GPU model,
    the machine's price shared over its GPUs (as bench shares a node's), or None when the host is
    unpriced or has no GPU. Owned hardware keeps its formula, so the bundle shows the basis."""
    hardware = HostHardware.model_validate(row.hardware)
    if row.price is None or not hardware.gpus:
        return None
    price = HostCreate.model_validate({"name": row.name, "price": row.price}).price
    count = len(hardware.gpus)
    entry: dict[str, float]
    if isinstance(price, GivenPrice):
        entry = {"usd_per_hour": price.usd_per_hour / count}
    else:
        assert price is not None
        entry = {
            "purchase_usd": price.purchase_usd / count,
            "amortization_hours": price.amortization_hours,
            "watts": price.watts / count,
            "usd_per_kwh": price.usd_per_kwh,
        }
    return {"source": "static", "static": {"gpus": {hardware.gpus[0].model: entry}}}


class HostCreate(BaseModel):
    """POST /hosts and PUT /hosts/{id}."""

    model_config = ConfigDict(extra="forbid")

    name: str = Field(pattern=DNS_LABEL)
    note: str | None = Field(default=None, max_length=MAX_NOTE)
    hardware: HostHardware = Field(default_factory=HostHardware)
    price: HostPrice | None = None


class HostResource(HostCreate):
    id: str
    # The price per machine-hour and its basis, when the host is priced.
    usd_per_hour: float | None = None
    price_basis: str | None = None
    # The managed targets that run on it, by id.
    target_ids: list[str] = Field(default_factory=list)
    created_at: datetime
    updated_at: datetime


class HostInUseError(ValueError):
    """Targets still name the host."""


class HostService:
    def __init__(
        self, repository: RunRepository, clock: Callable[[], datetime] = lambda: datetime.now(UTC)
    ) -> None:
        self._repository = repository
        self._clock = clock

    def list_hosts(self) -> list[HostResource]:
        return [self._resource(row) for row in self._repository.list_hosts()]

    def get(self, host_id: str) -> HostResource:
        return self._resource(self._row(host_id))

    def create(self, raw: bytes) -> HostResource:
        body = self._checked(raw, current_id=None)
        now = self._clock()
        row = HostRow(
            id=new_ulid(), name=body.name, created_at=now, updated_at=now, **self._fields(body)
        )
        self._repository.insert_host(row)
        return self._resource(row)

    def update(self, host_id: str, raw: bytes) -> HostResource:
        self._row(host_id)
        body = self._checked(raw, current_id=host_id)
        row = self._repository.update_host(
            host_id, name=body.name, updated_at=self._clock(), **self._fields(body)
        )
        assert row is not None
        return self._resource(row)

    def set_hardware(self, host_id: str, machine: HostMachine) -> HostResource:
        """Keep what the host's launcher read (GET /machine) as the host's hardware."""
        self._row(host_id)
        row = self._repository.update_host(
            host_id,
            hardware=HostHardware.of(machine).model_dump(mode="json", exclude_none=True),
            updated_at=self._clock(),
        )
        assert row is not None
        return self._resource(row)

    def delete(self, host_id: str) -> None:
        self._row(host_id)
        users = self._repository.managed_targets_on_host(host_id)
        if users:
            names = ", ".join(sorted(row.name for row in users))
            raise HostInUseError(
                f"managed targets {names} run on this host: point them elsewhere first"
            )
        self._repository.delete_host(host_id)

    def exists(self, host_id: str) -> bool:
        return self._repository.get_host(host_id) is not None

    def _row(self, host_id: str) -> HostRow:
        row = self._repository.get_host(host_id)
        if row is None:
            raise TargetNotFoundError(f"no host {host_id}")
        return row

    def _checked(self, raw: bytes, current_id: str | None) -> HostCreate:
        body = parse_body(raw, HostCreate)
        other = self._repository.host_named(body.name)
        if other is not None and other.id != current_id:
            raise TargetConflictError(f"host {body.name} already exists")
        return body

    @staticmethod
    def _fields(body: HostCreate) -> dict[str, object]:
        return {
            "note": body.note,
            "hardware": body.hardware.model_dump(mode="json", exclude_none=True),
            "price": None if body.price is None else body.price.model_dump(mode="json"),
        }

    def _resource(self, row: HostRow) -> HostResource:
        body = HostCreate.model_validate(
            {"name": row.name, "note": row.note, "hardware": row.hardware, "price": row.price}
        )
        usd_per_hour, basis = hourly(body.price) if body.price is not None else (None, None)
        return HostResource(
            **body.model_dump(),
            id=row.id,
            usd_per_hour=usd_per_hour,
            price_basis=basis,
            target_ids=[target.id for target in self._repository.managed_targets_on_host(row.id)],
            created_at=row.created_at,
            updated_at=row.updated_at,
        )


__all__ = ["HostInUseError", "HostResource", "HostService", "TargetInputError"]
