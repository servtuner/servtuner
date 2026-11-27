"""Tuning sessions; the database's unique index is the one-session lease."""

from sqlalchemy import select
from sqlalchemy.exc import IntegrityError

from servtuner_api.database import TuningSessionRow
from servtuner_api.records.base import ACTIVE_SESSION_STATUSES, Records


class TuningRecords(Records):
    """Tuning sessions; the database's unique index is the one-session lease."""

    def insert_tuning_session(self, session_row: TuningSessionRow) -> bool:
        """Insert a session; False when another session is active (the lease index)."""
        try:
            with self._sessions.begin() as session:
                session.add(session_row)
        except IntegrityError:
            return False
        return True

    def get_tuning_session(self, session_id: str) -> TuningSessionRow | None:
        with self._sessions() as session:
            return session.get(TuningSessionRow, session_id)

    def list_tuning_sessions(self, *, active_only: bool = False) -> list[TuningSessionRow]:
        with self._sessions() as session:
            query = select(TuningSessionRow).order_by(TuningSessionRow.created_at.desc())
            if active_only:
                query = query.where(TuningSessionRow.status.in_(ACTIVE_SESSION_STATUSES))
            return list(session.scalars(query))

    def update_tuning_session(self, session_id: str, **fields: object) -> TuningSessionRow | None:
        with self._sessions.begin() as session:
            row = session.get(TuningSessionRow, session_id, with_for_update=True)
            if row is None:
                return None
            for name, value in fields.items():
                setattr(row, name, value)
            session.flush()
            session.expunge(row)
            return row
