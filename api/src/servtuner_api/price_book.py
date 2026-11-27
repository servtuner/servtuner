"""The install's price book: what this install's machines cost, set once.

New runs and tuning sessions copy it into the RunSpec as ``analysis.infra_pricing``, so a run
still carries the prices it used (a later change never rewrites old runs) and bench snapshots
them into pricing/infra.json. Hosts keep their own price: a run on a host target is priced
from its host, never from here.

It also holds the token-price datasheet an operator supplies: an uploaded file the api serves
to runs (GET /runs/{id}/datasheet), or the URL of an internal copy. servtuner bundles no price data.
"""

import hashlib
import json
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Annotated, Literal

from pydantic import AnyUrl, BaseModel, ConfigDict, Field, StringConstraints, model_validator

from servtuner_api.auth.attribution import acting_user_id
from servtuner_api.database import PriceBookDatasheetRow
from servtuner_api.records.repository import RunRepository
from servtuner_api.targets.common import TargetInputError, parse_body

# bench refuses a larger datasheet (internal/pricing/datasheet.go maxDatasheetBytes).
MAX_DATASHEET_BYTES = 67108864  # 64 MiB

MAX_KEY = 256
PriceKey = Annotated[
    str, StringConstraints(min_length=1, max_length=MAX_KEY, strip_whitespace=True)
]


class NodeTypePrice(BaseModel):
    """A node type (node.kubernetes.io/instance-type) per hour, with what a pod can be given, so
    bench can price a pod's share of it."""

    model_config = ConfigDict(extra="forbid")

    usd_per_hour: float = Field(ge=0)
    cores: float = Field(gt=0)
    memory_gib: float = Field(gt=0)
    gpus: int = Field(default=0, ge=0)


class GpuModelPrice(BaseModel):
    """One GPU of this model per hour (the model the snapshot records as the accelerator)."""

    model_config = ConfigDict(extra="forbid")

    usd_per_hour: float = Field(ge=0)


class PriceBook(BaseModel):
    """PUT /price-book. ``static`` prices from the tables below; ``aws`` asks the AWS Price List
    for the node a run lands on, in ``aws_region``; ``none`` leaves new runs unpriced."""

    model_config = ConfigDict(extra="forbid")

    source: Literal["none", "static", "aws"] = "none"
    # Spot, reserved instances or a Savings Plan, as one multiplier on every price.
    discount: float = Field(default=1, gt=0, le=1)
    nodes: dict[PriceKey, NodeTypePrice] = Field(default_factory=dict)
    gpus: dict[PriceKey, GpuModelPrice] = Field(default_factory=dict)
    aws_region: str | None = Field(default=None, pattern=r"^[a-z]{2}(-[a-z]+)+-\d+$")
    # An internal copy of the token-price datasheet (LiteLLM's model-price JSON), instead of an
    # upload. Runs fetch it themselves, so bench must reach it.
    datasheet_url: AnyUrl | None = None

    @model_validator(mode="after")
    def _complete(self) -> PriceBook:
        if self.source == "static" and not self.nodes and not self.gpus:
            raise ValueError("a static price book needs at least one node type or GPU model")
        if self.source == "aws" and self.aws_region is None:
            raise ValueError("an aws price book needs aws_region, e.g. us-east-1")
        return self


class DatasheetView(BaseModel):
    """The price book's datasheet: an upload (with what it holds) or a URL."""

    source: Literal["upload", "url"]
    url: str | None = None
    sha256: str | None = None
    size_bytes: int | None = None
    entries: int | None = None
    priced_entries: int | None = None
    uploaded_at: datetime | None = None
    uploaded_by: str | None = None


class PriceBookResource(PriceBook):
    # The RunSpec analysis.infra_pricing new runs get, or absent while the source is none.
    infra_pricing: dict[str, object] | None = None
    # The token_pricing block a run priced by the datasheet gets, or absent without one.
    token_pricing: dict[str, object] | None = None
    datasheet: DatasheetView | None = None
    # The install has no internet access: aws is refused and no public datasheet is fetched.
    offline: bool = False
    updated_at: datetime | None = None
    updated_by: str | None = None


def infra_pricing(book: PriceBook) -> dict[str, object] | None:
    """The RunSpec analysis.infra_pricing block for this book, or None when it prices nothing."""
    block: dict[str, object]
    if book.source == "static":
        table: dict[str, object] = {}
        if book.nodes:
            table["nodes"] = {
                key: price.model_dump(exclude_defaults=True) for key, price in book.nodes.items()
            }
        if book.gpus:
            table["gpus"] = {key: price.model_dump() for key, price in book.gpus.items()}
        block = {"source": "static", "static": table}
    elif book.source == "aws":
        assert book.aws_region is not None
        block = {"source": "aws", "aws": {"region": book.aws_region}}
    else:
        return None
    if book.discount != 1:
        block["discount"] = book.discount
    return block


def token_pricing(book: PriceBook, uploaded: bool) -> dict[str, object] | None:
    """The RunSpec analysis.token_pricing block for a run priced by the book's datasheet: the
    upload, fetched from the api, or the book's URL. None when the book names none."""
    if uploaded:
        return {"source": "datasheet", "from_price_book": True}
    if book.datasheet_url is not None:
        return {"source": "datasheet", "url": str(book.datasheet_url)}
    return None


def check_datasheet(raw: bytes) -> tuple[int, int]:
    """(entries, entries with a token price) of a datasheet bench can read, or the reasons it
    cannot: a JSON object of objects whose token costs are numbers (bench's ParseDatasheet)."""
    if len(raw) > MAX_DATASHEET_BYTES:
        raise TargetInputError([f"the datasheet is over {MAX_DATASHEET_BYTES} bytes"])
    try:
        sheet = json.loads(raw)
    except ValueError:
        raise TargetInputError(["the datasheet is not JSON"]) from None
    if not isinstance(sheet, dict):
        raise TargetInputError(["the datasheet must be a JSON object of models"])
    priced = 0
    problems: list[str] = []
    for key, entry in sheet.items():
        if not isinstance(entry, dict):
            problems.append(f"{key}: not an object")
            continue
        costs = [entry.get(name) for name in ("input_cost_per_token", "output_cost_per_token")]
        if any(cost is not None and not isinstance(cost, int | float) for cost in costs):
            problems.append(f"{key}: a token cost is not a number")
        elif any(cost is not None for cost in costs):
            priced += 1
    if problems:
        raise TargetInputError(problems[:10])
    if priced == 0:
        raise TargetInputError(["the datasheet prices no model (no input_cost_per_token)"])
    return len(sheet), priced


class PriceBookService:
    def __init__(
        self,
        repository: RunRepository,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
        offline: bool = False,
    ) -> None:
        self._repository = repository
        self._clock = clock
        self.offline = offline

    def book(self) -> PriceBook:
        row = self._repository.get_price_book()
        return PriceBook() if row is None else PriceBook.model_validate(row.book)

    def infra_pricing(self) -> dict[str, object] | None:
        return infra_pricing(self.book())

    def token_pricing(self) -> dict[str, object] | None:
        return token_pricing(self.book(), self._repository.get_datasheet() is not None)

    def get(self) -> PriceBookResource:
        row = self._repository.get_price_book()
        book = self.book()
        upload = self._repository.get_datasheet()
        datasheet = None
        if upload is not None:
            datasheet = DatasheetView(
                source="upload",
                sha256=upload.sha256,
                size_bytes=upload.size_bytes,
                entries=upload.entries,
                priced_entries=upload.priced_entries,
                uploaded_at=upload.uploaded_at,
                uploaded_by=upload.uploaded_by,
            )
        elif book.datasheet_url is not None:
            datasheet = DatasheetView(source="url", url=str(book.datasheet_url))
        return PriceBookResource(
            **book.model_dump(),
            infra_pricing=infra_pricing(book),
            token_pricing=token_pricing(book, upload is not None),
            datasheet=datasheet,
            offline=self.offline,
            updated_at=None if row is None else row.updated_at,
            updated_by=None if row is None else row.updated_by,
        )

    def save(self, raw: bytes) -> PriceBookResource:
        book = parse_body(raw, PriceBook)
        if self.offline and book.source == "aws":
            raise TargetInputError(
                [
                    "source: this install is offline, so the AWS Price List is not reachable;"
                    " give static prices"
                ]
            )
        if book.datasheet_url is not None and self._repository.get_datasheet() is not None:
            raise TargetInputError(
                ["datasheet_url: remove the uploaded datasheet first (one datasheet at a time)"]
            )
        self._repository.save_price_book(
            book.model_dump(mode="json", exclude_none=True), self._clock(), acting_user_id.get()
        )
        return self.get()

    def save_datasheet(self, raw: bytes) -> PriceBookResource:
        """Keep an uploaded datasheet, byte for byte, after checking bench can read it."""
        if self.book().datasheet_url is not None:
            raise TargetInputError(
                ["the price book names a datasheet_url: clear it first (one datasheet at a time)"]
            )
        entries, priced = check_datasheet(raw)
        self._repository.save_datasheet(
            PriceBookDatasheetRow(
                content=raw,
                sha256=hashlib.sha256(raw).hexdigest(),
                size_bytes=len(raw),
                entries=entries,
                priced_entries=priced,
                uploaded_at=self._clock(),
                uploaded_by=acting_user_id.get(),
            )
        )
        return self.get()

    def delete_datasheet(self) -> bool:
        return self._repository.delete_datasheet()

    def datasheet_content(self) -> bytes | None:
        return self._repository.get_datasheet_content()
