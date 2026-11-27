"""Runs: insert, read, the lifecycle (events and runner statuses), deletes and run_metrics."""

from datetime import datetime

from sqlalchemy import delete, exists, select
from sqlalchemy.dialects.postgresql import insert

from servtuner_api.auth.attribution import acting_user_id
from servtuner_api.database import (
    ExperimentAnalysisRow,
    ExperimentRow,
    RunEventRow,
    RunMetricRow,
    RunRow,
    UserRow,
)
from servtuner_api.indexing import MetricRow
from servtuner_api.models import Event
from servtuner_api.records.base import (
    EVENT_STATE_TO_STATUS,
    FINAL_STATUSES,
    Records,
    RunFilters,
    RunStatus,
    is_final,
    utc_now,
)
from servtuner_api.runner import RunnerState, RunnerStatus


class RunRecords(Records):
    """Runs: insert, read, the lifecycle (events and runner statuses), deletes and run_metrics."""

    def insert(self, run: RunRow) -> None:
        with self._sessions.begin() as session:
            if run.experiment is not None:
                session.execute(
                    insert(ExperimentRow)
                    .values(
                        name=run.experiment,
                        created_at=run.created_at,
                        created_by=acting_user_id.get(),
                    )
                    .on_conflict_do_nothing()
                )
            session.add(run)

    def usernames(self, user_ids: set[str]) -> dict[str, str]:
        """Each user id's username, in one query, for showing who created what."""
        if not user_ids:
            return {}
        query = select(UserRow.id, UserRow.username).where(UserRow.id.in_(user_ids))
        with self._sessions() as session:
            return {user_id: username for user_id, username in session.execute(query)}

    def get(self, run_id: str) -> RunRow | None:
        with self._sessions() as session:
            return session.get(RunRow, run_id)

    def list_runs(self, filters: RunFilters, limit: int) -> list[RunRow]:
        query = select(RunRow).order_by(RunRow.id.desc()).limit(limit)
        if filters.experiment is not None:
            query = query.where(RunRow.experiment == filters.experiment)
        if filters.target_type is not None:
            query = query.where(RunRow.target_type == filters.target_type)
        if filters.status is not None:
            query = query.where(RunRow.status == filters.status.value)
        if filters.label is not None:
            key, value = filters.label
            query = query.where(RunRow.labels.contains({key: value}))
        with self._sessions() as session:
            return list(session.scalars(query))

    def list_events(self, run_id: str) -> list[RunEventRow]:
        query = select(RunEventRow).where(RunEventRow.run_id == run_id)
        with self._sessions() as session:
            return list(session.scalars(query.order_by(RunEventRow.sequence)))

    def release_awaiting(self, run_id: str) -> bool:
        """Move an awaiting_config run to pending; False when the run is not awaiting (already
        released, or never was)."""
        with self._sessions.begin() as session:
            run = self._locked(session, run_id)
            if run.status != RunStatus.AWAITING_CONFIG:
                return False
            run.status = RunStatus.PENDING
            return True

    def mark_launching(self, run_id: str, token_hash: str, token_expires_at: datetime) -> None:
        """Pending to launching, with the run token of this launch."""
        with self._sessions.begin() as session:
            run = self._locked(session, run_id)
            if run.status == RunStatus.PENDING:
                run.status = RunStatus.LAUNCHING
            run.token_hash, run.token_expires_at = token_hash, token_expires_at

    def apply_runner_status(self, run_id: str, runner_status: RunnerStatus) -> None:
        with self._sessions.begin() as session:
            run = self._locked(session, run_id)
            if is_final(run.status):
                return
            if runner_status.state is RunnerState.RUNNING:
                self._begin_running(run)
                return
            run.status = RunStatus(runner_status.state.value)
            run.exit_code = runner_status.exit_code
            run.failure_message = runner_status.message
            run.finished_at = utc_now()

    def mark_launch_failed(self, run_id: str, message: str) -> None:
        self.apply_runner_status(run_id, RunnerStatus(RunnerState.FAILED, message=message))

    def record_event(self, event: Event, event_time: datetime) -> bool:
        """Store the event; return False for a duplicate sequence (retried POST)."""
        with self._sessions.begin() as session:
            run = self._locked(session, event.run_id)
            inserted = session.execute(
                insert(RunEventRow)
                .values(
                    run_id=event.run_id,
                    sequence=event.sequence,
                    time=event_time,
                    step=event.step.value,
                    state=event.state.value,
                    phase=event.phase,
                    message=event.message,
                )
                .on_conflict_do_nothing(index_elements=["run_id", "sequence"])
                .returning(RunEventRow.id)
            )
            if inserted.first() is None:
                return False
            latest_sequence = session.scalar(
                select(RunEventRow.sequence)
                .where(RunEventRow.run_id == event.run_id)
                .order_by(RunEventRow.sequence.desc())
                .limit(1)
            )
            # An old event arriving late must not roll the status back.
            if event.sequence == latest_sequence and not is_final(run.status):
                self._apply_event_status(run, event)
            return True

    def delete_run(self, run_id: str, analysis_ids: list[str]) -> bool:
        """A user's delete: the analyses that included the run, then the run, whose
        events and metrics cascade; one transaction, so a delete happens whole or not at all."""
        with self._sessions.begin() as session:
            if analysis_ids:
                session.execute(
                    delete(ExperimentAnalysisRow).where(ExperimentAnalysisRow.id.in_(analysis_ids))
                )
            row = session.get(RunRow, run_id)
            if row is None:
                return False
            session.delete(row)
            return True

    def replace_metrics(self, run_id: str, rows: list[MetricRow]) -> None:
        """Replace the run's run_metrics rows: the index is derived, never merged.

        The run's row is locked first, so two indexers of one run (the watcher
        and the startup backfill) take turns instead of both inserting.
        """
        with self._sessions.begin() as session:
            self._locked(session, run_id)
            session.execute(delete(RunMetricRow).where(RunMetricRow.run_id == run_id))
            session.add_all(
                RunMetricRow(
                    run_id=run_id,
                    stage=row.stage,
                    metric=row.metric,
                    stat=row.stat,
                    value=row.value,
                )
                for row in rows
            )

    def list_metrics(self, run_ids: list[str]) -> list[RunMetricRow]:
        query = select(RunMetricRow).where(RunMetricRow.run_id.in_(run_ids))
        with self._sessions() as session:
            return list(session.scalars(query))

    def final_runs_without_metrics(self) -> list[RunRow]:
        """Finished runs with no run_metrics rows: indexed by an older api, or not analyzable."""
        has_metrics = exists().where(RunMetricRow.run_id == RunRow.id)
        query = (
            select(RunRow)
            .where(RunRow.status.in_([status.value for status in FINAL_STATUSES]))
            .where(~has_metrics)
            .order_by(RunRow.id)
        )
        with self._sessions() as session:
            return list(session.scalars(query))

    @staticmethod
    def _begin_running(run: RunRow) -> None:
        if run.status in (RunStatus.PENDING, RunStatus.LAUNCHING):
            run.status = RunStatus.RUNNING
        if run.started_at is None:
            run.started_at = utc_now()

    def _apply_event_status(self, run: RunRow, event: Event) -> None:
        new_status = EVENT_STATE_TO_STATUS.get(event.state)
        if new_status is None:
            return
        self._begin_running(run)
        run.status = new_status
        run.phase = event.phase
