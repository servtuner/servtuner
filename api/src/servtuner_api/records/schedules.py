"""Schedules, and claiming the due ones so two replicas never launch one slot."""

from datetime import datetime

from sqlalchemy import select

from servtuner_api.database import RunRow, ScheduleRow
from servtuner_api.records.base import Records, is_final
from servtuner_api.schedules import next_slot


class ScheduleRecords(Records):
    """Schedules, and claiming the due ones so two replicas never launch one slot."""

    def insert_schedule(self, schedule: ScheduleRow) -> None:
        with self._sessions.begin() as session:
            session.add(schedule)

    def get_schedule(self, schedule_id: str) -> ScheduleRow | None:
        with self._sessions() as session:
            return session.get(ScheduleRow, schedule_id)

    def list_schedules(self) -> list[ScheduleRow]:
        with self._sessions() as session:
            return list(session.scalars(select(ScheduleRow).order_by(ScheduleRow.id.desc())))

    def delete_schedule(self, schedule_id: str) -> bool:
        with self._sessions.begin() as session:
            row = session.get(ScheduleRow, schedule_id)
            if row is None:
                return False
            session.delete(row)
            return True

    def update_schedule(self, schedule_id: str, **fields: object) -> ScheduleRow | None:
        with self._sessions.begin() as session:
            row = session.get(ScheduleRow, schedule_id, with_for_update=True)
            if row is None:
                return None
            for name, value in fields.items():
                setattr(row, name, value)
            session.flush()
            session.expunge(row)
            return row

    def claim_due_schedules(self, now: datetime) -> list[tuple[str, bytes]]:
        """Advance every due schedule past ``now``; return (id, spec) for each to launch.

        Rows are locked with SKIP LOCKED and advanced before anything launches,
        so two api replicas never launch one slot twice. A schedule whose last
        run is not final launches nothing this slot and says so in last_note.
        """
        query = (
            select(ScheduleRow)
            .where(ScheduleRow.is_enabled, ScheduleRow.next_run_at <= now)
            .with_for_update(skip_locked=True)
        )
        claims: list[tuple[str, bytes]] = []
        with self._sessions.begin() as session:
            for schedule in session.scalars(query):
                due = schedule.next_run_at
                schedule.next_run_at = next_slot(due, schedule.every_seconds, now)
                last = session.get(RunRow, schedule.last_run_id) if schedule.last_run_id else None
                if last is not None and not is_final(last.status):
                    schedule.last_note = (
                        f"slot {due.isoformat()} skipped: run {last.id} was still {last.status}"
                    )
                    continue
                claims.append((schedule.id, schedule.spec_raw))
        return claims
