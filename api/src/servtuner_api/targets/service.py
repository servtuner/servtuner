"""Saved targets and their write-only keys."""

import logging
from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any

from servtuner_api.database import TargetRow
from servtuner_api.ids import new_ulid
from servtuner_api.price_book import PriceBook, token_pricing
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.targets import (
    CredentialInput,
    SavedTargetCheck,
    TargetCheckView,
    TargetCreate,
    TargetResource,
    UnsavedTargetCheck,
)
from servtuner_api.secret_store import SecretStore
from servtuner_api.targets.checker import TargetChecker
from servtuner_api.targets.common import (
    ONE_TIME_SECRET,
    PASSWORD_KEY,
    TOKEN_KEY,
    USERNAME_KEY,
    SecretStoreUnavailableError,
    Slot,
    TargetConflictError,
    TargetInputError,
    TargetNotFoundError,
    apply_credentials,
    check_target_shape,
    managed_secret_name,
    masked_preview,
    parse_body,
    slots_for,
)
from servtuner_api.targets.pricing import HostHourly, NodeTypeHourly, TargetPricing, run_pricing

logger = logging.getLogger(__name__)


class TargetService:
    """Saved targets: CRUD, write-only credentials and checks."""

    def __init__(
        self,
        repository: RunRepository,
        store: SecretStore | None,
        checker: TargetChecker,
        default_prometheus: dict[str, Any] | None = None,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
    ) -> None:
        self._repository = repository
        self._store = store
        self._checker = checker
        self._default_prometheus = default_prometheus
        self._clock = clock

    # --- targets

    def list_targets(self) -> list[TargetRow]:
        return self._repository.list_targets()

    def get(self, target_id: str) -> TargetRow:
        row = self._repository.get_target(target_id)
        if row is None:
            raise TargetNotFoundError(f"no target {target_id}")
        return row

    def create(self, raw: bytes) -> TargetRow:
        body = parse_body(raw, TargetCreate)
        target = self._checked_target(body)
        if self._repository.target_named(body.name) is not None:
            raise TargetConflictError(f"a target named {body.name} already exists")
        now = self._clock()
        row = TargetRow(
            id=new_ulid(),
            name=body.name,
            description=body.description,
            target_type=target["type"],
            target=target,
            pricing=self._checked_pricing(body),
            credentials={},
            last_check=None,
            created_at=now,
            updated_at=now,
        )
        self._repository.insert_target(row)
        return row

    def update(self, target_id: str, raw: bytes) -> TargetRow:
        current = self.get(target_id)
        body = parse_body(raw, TargetCreate)
        target = self._checked_target(body)
        other = self._repository.target_named(body.name)
        if other is not None and other.id != target_id:
            raise TargetConflictError(f"a target named {body.name} already exists")
        allowed = set(slots_for(target["type"]))
        dropped = [slot for slot in current.credentials if slot not in allowed]
        for slot in dropped:
            self._forget(current.credentials[slot])
        credentials = {k: v for k, v in current.credentials.items() if k in allowed}
        updated = self._repository.update_target(
            target_id,
            name=body.name,
            description=body.description,
            target_type=target["type"],
            target=apply_credentials(target, credentials),
            pricing=self._checked_pricing(body),
            credentials=credentials,
            updated_at=self._clock(),
        )
        assert updated is not None
        return updated

    def resource(self, row: TargetRow) -> TargetResource:
        """The saved target with what its runs get from its prices."""
        pricing = None if row.pricing is None else TargetPricing.model_validate(row.pricing)
        if pricing is None:
            return TargetResource.from_row(row, slots_for(row.target_type))
        host = (
            self._repository.get_host(pricing.hourly.host_id)
            if isinstance(pricing.hourly, HostHourly)
            else None
        )
        row_book = self._repository.get_price_book()
        book = PriceBook() if row_book is None else PriceBook.model_validate(row_book.book)
        uploaded = self._repository.get_datasheet() is not None
        priced = run_pricing(pricing, host, book, token_pricing(book, uploaded))
        return TargetResource.from_row(row, slots_for(row.target_type), priced)

    def _checked_pricing(self, body: TargetCreate) -> dict[str, Any] | None:
        """What the prices name must exist when they are saved: the host, or the node type in
        the price book."""
        if body.pricing is None or (body.pricing.hourly is None and body.pricing.token is None):
            return None
        hourly = body.pricing.hourly
        if isinstance(hourly, HostHourly) and self._repository.get_host(hourly.host_id) is None:
            raise TargetInputError([f"pricing.hourly.host_id: no host {hourly.host_id}"])
        if isinstance(hourly, NodeTypeHourly):
            row = self._repository.get_price_book()
            book = PriceBook() if row is None else PriceBook.model_validate(row.book)
            if hourly.node_type not in book.nodes:
                problem = f"the price book has no node type {hourly.node_type}"
                raise TargetInputError([f"pricing.hourly.node_type: {problem}"])
        return body.pricing.model_dump(mode="json", exclude_none=True)

    def delete(self, target_id: str) -> None:
        row = self.get(target_id)
        for stored in row.credentials.values():
            self._forget(stored)
        self._repository.delete_target(target_id)

    def _checked_target(self, body: TargetCreate) -> dict[str, Any]:
        target = body.target.model_dump(mode="json", exclude_none=True)
        if "auth" in target or "admin" in target:
            raise TargetInputError(
                [
                    "target.auth and target.admin are set with "
                    "PUT /targets/{id}/credentials/{slot}, never in the target body"
                ]
            )
        check_target_shape(target)
        return target

    # --- credentials

    def set_credential(self, target_id: str, slot_name: str, raw: bytes) -> TargetRow:
        row = self.get(target_id)
        slot = self._slot(row, slot_name)
        body = parse_body(raw, CredentialInput)
        stored = self._store_credential(row, slot, body)
        previous = row.credentials.get(slot.value)
        if previous is not None and previous["secret_name"] != stored["secret_name"]:
            self._forget(previous)
        credentials = {**row.credentials, slot.value: stored}
        updated = self._repository.update_target(
            target_id,
            credentials=credentials,
            target=apply_credentials(row.target, credentials),
            updated_at=self._clock(),
        )
        assert updated is not None
        logger.info("target %s: %s replaced (%s)", row.name, slot.value, stored["source"])
        return updated

    def clear_credential(self, target_id: str, slot_name: str) -> TargetRow:
        row = self.get(target_id)
        slot = self._slot(row, slot_name)
        stored = row.credentials.get(slot.value)
        if stored is not None:
            self._forget(stored)
        credentials = {k: v for k, v in row.credentials.items() if k != slot.value}
        updated = self._repository.update_target(
            target_id,
            credentials=credentials,
            target=apply_credentials(row.target, credentials),
            updated_at=self._clock(),
        )
        assert updated is not None
        logger.info("target %s: %s removed", row.name, slot.value)
        return updated

    def _slot(self, row: TargetRow, slot_name: str) -> Slot:
        if slot_name not in slots_for(row.target_type):
            allowed = ", ".join(slots_for(row.target_type))
            raise TargetInputError([f"a {row.target_type} target has the slots {allowed}"])
        return Slot(slot_name)

    def _store_credential(
        self, row: TargetRow, slot: Slot, body: CredentialInput
    ) -> dict[str, Any]:
        now = self._clock().isoformat()
        forms = [
            body.value is not None,
            body.username is not None or body.password is not None,
            body.external is not None,
        ]
        if sum(forms) != 1:
            raise TargetInputError(
                ["give exactly one of value, username and password, or external"]
            )
        if body.external is not None:
            return {
                "source": "external",
                "secret_name": body.external.name,
                "form": self._external_form(slot, body),
                "keys": self._external_keys(slot, body),
                "set_at": now,
            }
        if self._store is None:
            raise SecretStoreUnavailableError(
                "no secret store is configured (SERVTUNER_SECRET_STORE), so servtuner cannot keep "
                "keys; name a Secret you manage with external instead"
            )
        name = managed_secret_name(row.id, slot)
        if body.value is not None:
            value = body.value.get_secret_value()
            self._store.put(name, {TOKEN_KEY: value}, row.id)
            keys = {"key": TOKEN_KEY} if slot is Slot.API_KEY else {"tokenKey": TOKEN_KEY}
            return {
                "source": "managed",
                "secret_name": name,
                "form": "token",
                "keys": keys,
                "preview": masked_preview(value),
                "set_at": now,
            }
        if slot is not Slot.ADMIN or body.username is None or body.password is None:
            raise TargetInputError(["username and password: both, and for the admin slot only"])
        password = body.password.get_secret_value()
        self._store.put(name, {USERNAME_KEY: body.username, PASSWORD_KEY: password}, row.id)
        return {
            "source": "managed",
            "secret_name": name,
            "form": "basic",
            "keys": {"usernameKey": USERNAME_KEY, "passwordKey": PASSWORD_KEY},
            "username": body.username,
            "preview": masked_preview(password),
            "set_at": now,
        }

    @staticmethod
    def _external_form(slot: Slot, body: CredentialInput) -> str:
        assert body.external is not None
        return "basic" if slot is Slot.ADMIN and body.external.username_key else "token"

    @staticmethod
    def _external_keys(slot: Slot, body: CredentialInput) -> dict[str, str]:
        external = body.external
        assert external is not None
        if slot is Slot.API_KEY:
            if not external.key or external.token_key or external.username_key:
                raise TargetInputError(["external: an api_key names its Secret's key with key"])
            return {"key": external.key}
        if external.token_key and not (external.username_key or external.password_key):
            return {"tokenKey": external.token_key}
        if external.username_key and external.password_key and not external.token_key:
            return {"usernameKey": external.username_key, "passwordKey": external.password_key}
        raise TargetInputError(
            ["external: admin names token_key, or username_key and password_key"]
        )

    def _forget(self, stored: dict[str, Any]) -> None:
        """Delete a managed Secret; an external one belongs to the user and stays."""
        if stored["source"] == "managed" and self._store is not None:
            self._store.delete(stored["secret_name"])

    # --- checks

    def check_saved(self, target_id: str, raw: bytes) -> TargetCheckView:
        row = self.get(target_id)
        body = parse_body(raw or b"{}", SavedTargetCheck)
        target = dict(row.target)
        one_time = None
        if body.api_key is not None:
            target["auth"] = {"secretRef": {"name": ONE_TIME_SECRET, "key": TOKEN_KEY}}
            one_time = (ONE_TIME_SECRET, body.api_key.get_secret_value())
        request: dict[str, Any] = {"target": target, "generate": body.generate}
        if self._default_prometheus is not None:
            request["prometheus"] = self._default_prometheus
        view = self._checker.check(request, one_time)
        if one_time is None:
            # A one-time key's result says nothing about the stored key: not kept.
            self._repository.update_target(target_id, last_check=view.model_dump(mode="json"))
        return view

    def check_unsaved(self, raw: bytes) -> TargetCheckView:
        body = parse_body(raw, UnsavedTargetCheck)
        request = body.model_dump(mode="json", exclude_none=True)
        check_target_shape(request["target"], request.get("prometheus"))
        if "prometheus" not in request and self._default_prometheus is not None:
            request["prometheus"] = self._default_prometheus
        return self._checker.check(request)
