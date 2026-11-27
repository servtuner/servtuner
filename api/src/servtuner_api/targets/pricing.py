"""How a saved target is paid for.

A target servtuner does not manage has no Kubernetes node to price it by, so its owner says:
- **by the hour:** the host it runs on (the host's hardware and price), or a node type
  from the price book with the GPUs it uses;
- **by the token:** the datasheet, or prices given per million tokens (a private API).

A router can have both; bench keeps them apart (mixed topologies). New run
copies ``run_pricing``'s hardware into ``target.hardware`` and its blocks into ``analysis``,
so each run still carries the prices it used.
"""

from dataclasses import dataclass, field
from typing import Annotated, Literal

from pydantic import AnyUrl, BaseModel, ConfigDict, Field

from servtuner_api.database import HostRow
from servtuner_api.hosts import declared_hardware, infra_pricing
from servtuner_api.price_book import PriceBook

MAX_NAME = 256


class HostHourly(BaseModel):
    """Priced as the host it runs on: the host's hardware and price per hour."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["host"]
    host_id: str = Field(min_length=26, max_length=26)


class NodeTypeHourly(BaseModel):
    """Priced as the GPUs it holds on a node type from the price book (node price ÷ its GPUs,
    or the GPU model's own price when the book has one)."""

    model_config = ConfigDict(extra="forbid")

    kind: Literal["node_type"]
    node_type: str = Field(min_length=1, max_length=MAX_NAME)
    # Without GPUs a node is priced by the CPU and memory a replica requests, which a target
    # outside Kubernetes does not have: such a machine is a host.
    gpu_count: int = Field(ge=1)
    gpu_model: str | None = Field(default=None, min_length=1, max_length=MAX_NAME)


class DatasheetTokens(BaseModel):
    """Token prices from the datasheet bench fetches at the start of each run."""

    model_config = ConfigDict(extra="forbid")

    source: Literal["datasheet"]
    url: AnyUrl | None = None


class ManualTokens(BaseModel):
    """Token prices as given, for an API no datasheet lists."""

    model_config = ConfigDict(extra="forbid")

    source: Literal["manual"]
    usd_per_1m_input_tokens: float = Field(ge=0)
    usd_per_1m_output_tokens: float = Field(ge=0)


class TargetPricing(BaseModel):
    """A saved target's prices: either or both."""

    model_config = ConfigDict(extra="forbid")

    hourly: Annotated[HostHourly | NodeTypeHourly, Field(discriminator="kind")] | None = None
    token: Annotated[DatasheetTokens | ManualTokens, Field(discriminator="source")] | None = None


@dataclass
class RunPricing:
    """What a run against the target gets: its declared hardware and its analysis blocks, and
    why something it names cannot price it now (a host deleted or unpriced since, say)."""

    hardware: dict[str, object] | None = None
    analysis: dict[str, object] = field(default_factory=dict)
    problems: list[str] = field(default_factory=list)


def run_pricing(
    pricing: TargetPricing,
    host: HostRow | None,
    book: PriceBook,
    book_tokens: dict[str, object] | None = None,
) -> RunPricing:
    """book_tokens is the price book's datasheet block (price_book.token_pricing): a datasheet
    target that names no URL of its own uses it."""
    result = RunPricing()
    hourly = pricing.hourly
    if isinstance(hourly, HostHourly):
        if host is None:
            result.problems.append(f"host {hourly.host_id} no longer exists")
        else:
            result.hardware = declared_hardware(host)
            host_block = infra_pricing(host)
            if result.hardware is None:
                result.problems.append(f"host {host.name} has no hardware recorded")
            elif host_block is None:
                result.problems.append(f"host {host.name} has no price or no GPU")
            else:
                result.analysis["infra_pricing"] = host_block
    elif isinstance(hourly, NodeTypeHourly):
        node = book.nodes.get(hourly.node_type)
        if node is None:
            result.problems.append(f"the price book has no node type {hourly.node_type}")
        else:
            result.hardware = {
                "host": hourly.node_type,
                "accelerator": hourly.gpu_model or "unknown",
                "gpu_count": hourly.gpu_count,
            }
            table: dict[str, object] = {
                "nodes": {hourly.node_type: node.model_dump(exclude_defaults=True)}
            }
            if hourly.gpu_model and hourly.gpu_model in book.gpus:
                table["gpus"] = {hourly.gpu_model: book.gpus[hourly.gpu_model].model_dump()}
            block: dict[str, object] = {"source": "static", "static": table}
            if book.discount != 1:
                block["discount"] = book.discount
            result.analysis["infra_pricing"] = block
    token = pricing.token
    if isinstance(token, DatasheetTokens):
        if token.url is None and book_tokens is not None:
            result.analysis["token_pricing"] = dict(book_tokens)
        else:
            result.analysis["token_pricing"] = token.model_dump(mode="json", exclude_none=True)
    elif isinstance(token, ManualTokens):
        result.analysis["token_pricing"] = {
            "source": "manual",
            "manual": token.model_dump(exclude={"source"}),
        }
    return result
