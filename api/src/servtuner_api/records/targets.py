"""Saved targets, managed targets, hosts and the price book."""

from datetime import datetime
from typing import Any

from sqlalchemy import select

from servtuner_api.database import (
    HostRow,
    ManagedTargetRow,
    PriceBookDatasheetRow,
    PriceBookRow,
    TargetRow,
)
from servtuner_api.records.base import Records

PRICE_BOOK_ID = "install"  # the price book is one row


class TargetRecords(Records):
    """Saved targets, managed targets, hosts and the price book."""

    def insert_target(self, target: TargetRow) -> None:
        with self._sessions.begin() as session:
            session.add(target)

    def get_target(self, target_id: str) -> TargetRow | None:
        with self._sessions() as session:
            return session.get(TargetRow, target_id)

    def target_named(self, name: str) -> TargetRow | None:
        with self._sessions() as session:
            return session.scalars(select(TargetRow).where(TargetRow.name == name)).first()

    def list_targets(self) -> list[TargetRow]:
        with self._sessions() as session:
            return list(session.scalars(select(TargetRow).order_by(TargetRow.name)))

    def update_target(self, target_id: str, **fields: object) -> TargetRow | None:
        with self._sessions.begin() as session:
            row = session.get(TargetRow, target_id, with_for_update=True)
            if row is None:
                return None
            for name, value in fields.items():
                setattr(row, name, value)
            session.flush()
            session.expunge(row)
            return row

    def delete_target(self, target_id: str) -> bool:
        with self._sessions.begin() as session:
            row = session.get(TargetRow, target_id)
            if row is None:
                return False
            session.delete(row)
            return True

    def insert_managed_target(self, target: ManagedTargetRow) -> None:
        with self._sessions.begin() as session:
            session.add(target)

    def get_managed_target(self, target_id: str) -> ManagedTargetRow | None:
        with self._sessions() as session:
            return session.get(ManagedTargetRow, target_id)

    def managed_target_using(self, *, name: str, namespace: str) -> ManagedTargetRow | None:
        """A managed target with this name, or in this namespace (both are unique)."""
        with self._sessions() as session:
            return session.scalars(
                select(ManagedTargetRow).where(
                    (ManagedTargetRow.name == name) | (ManagedTargetRow.namespace == namespace)
                )
            ).first()

    def list_managed_targets(self) -> list[ManagedTargetRow]:
        with self._sessions() as session:
            return list(session.scalars(select(ManagedTargetRow).order_by(ManagedTargetRow.name)))

    def update_managed_target(self, target_id: str, **fields: object) -> ManagedTargetRow | None:
        with self._sessions.begin() as session:
            row = session.get(ManagedTargetRow, target_id, with_for_update=True)
            if row is None:
                return None
            for name, value in fields.items():
                setattr(row, name, value)
            session.flush()
            session.expunge(row)
            return row

    def delete_managed_target(self, target_id: str) -> bool:
        with self._sessions.begin() as session:
            row = session.get(ManagedTargetRow, target_id)
            if row is None:
                return False
            session.delete(row)
            return True

    def insert_host(self, host: HostRow) -> None:
        with self._sessions.begin() as session:
            session.add(host)

    def get_host(self, host_id: str) -> HostRow | None:
        with self._sessions() as session:
            return session.get(HostRow, host_id)

    def host_named(self, name: str) -> HostRow | None:
        with self._sessions() as session:
            return session.scalars(select(HostRow).where(HostRow.name == name)).first()

    def list_hosts(self) -> list[HostRow]:
        with self._sessions() as session:
            return list(session.scalars(select(HostRow).order_by(HostRow.name)))

    def update_host(self, host_id: str, **fields: object) -> HostRow | None:
        with self._sessions.begin() as session:
            row = session.get(HostRow, host_id, with_for_update=True)
            if row is None:
                return None
            for name, value in fields.items():
                setattr(row, name, value)
            session.flush()
            session.expunge(row)
            return row

    def delete_host(self, host_id: str) -> bool:
        with self._sessions.begin() as session:
            row = session.get(HostRow, host_id)
            if row is None:
                return False
            session.delete(row)
            return True

    def managed_targets_on_host(self, host_id: str) -> list[ManagedTargetRow]:
        """The host targets that name this host (spec.host_id)."""
        with self._sessions() as session:
            return list(
                session.scalars(
                    select(ManagedTargetRow)
                    .where(ManagedTargetRow.spec["host_id"].astext == host_id)
                    .order_by(ManagedTargetRow.name)
                )
            )

    def get_price_book(self) -> PriceBookRow | None:
        with self._sessions() as session:
            return session.get(PriceBookRow, PRICE_BOOK_ID)

    def save_price_book(
        self, book: dict[str, Any], updated_at: datetime, updated_by: str | None
    ) -> None:
        """Replace the one price book: the last writer wins, as with any settings form."""
        with self._sessions.begin() as session:
            row = session.get(PriceBookRow, PRICE_BOOK_ID, with_for_update=True)
            if row is None:
                session.add(
                    PriceBookRow(
                        id=PRICE_BOOK_ID, book=book, updated_at=updated_at, updated_by=updated_by
                    )
                )
                return
            row.book = book
            row.updated_at = updated_at
            row.updated_by = updated_by

    def get_datasheet(self) -> PriceBookDatasheetRow | None:
        """The uploaded datasheet's record, without its content."""
        with self._sessions() as session:
            return session.get(PriceBookDatasheetRow, PRICE_BOOK_ID)

    def get_datasheet_content(self) -> bytes | None:
        with self._sessions() as session:
            return session.scalar(
                select(PriceBookDatasheetRow.content).where(
                    PriceBookDatasheetRow.id == PRICE_BOOK_ID
                )
            )

    def save_datasheet(self, row: PriceBookDatasheetRow) -> None:
        """Replace the uploaded datasheet (the last upload wins)."""
        row.id = PRICE_BOOK_ID
        with self._sessions.begin() as session:
            session.merge(row)

    def delete_datasheet(self) -> bool:
        with self._sessions.begin() as session:
            row = session.get(PriceBookDatasheetRow, PRICE_BOOK_ID)
            if row is None:
                return False
            session.delete(row)
            return True
