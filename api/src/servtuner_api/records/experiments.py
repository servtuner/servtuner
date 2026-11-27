"""Experiments, their analyses, and saved scenarios."""

from sqlalchemy import delete, exists, func, select

from servtuner_api.database import (
    ExperimentAnalysisRow,
    ExperimentRow,
    RunMetricRow,
    RunRow,
    ScenarioRow,
    TuningSessionRow,
)
from servtuner_api.records.base import ExperimentListing, Records, RunStatus


class ExperimentRecords(Records):
    """Experiments, their analyses, and saved scenarios."""

    def list_experiments(self) -> list[ExperimentListing]:
        """Every experiment, newest first, with how many runs it has."""
        query = (
            select(
                ExperimentRow.name,
                ExperimentRow.created_at,
                func.count(RunRow.id),
                func.max(RunRow.created_at),
            )
            .outerjoin(RunRow, RunRow.experiment == ExperimentRow.name)
            .group_by(ExperimentRow.name, ExperimentRow.created_at)
            .order_by(ExperimentRow.created_at.desc(), ExperimentRow.name)
        )
        with self._sessions() as session:
            return [ExperimentListing(*row) for row in session.execute(query)]

    def get_experiment(self, name: str) -> ExperimentRow | None:
        with self._sessions() as session:
            return session.get(ExperimentRow, name)

    def runs_of_experiment(self, name: str) -> list[RunRow]:
        """All of the experiment's runs, oldest first, so repeats read in the order they ran."""
        query = select(RunRow).where(RunRow.experiment == name).order_by(RunRow.id)
        with self._sessions() as session:
            return list(session.scalars(query))

    def indexed_runs_of_experiment(self, name: str) -> list[RunRow]:
        """The experiment's succeeded runs that have run_metrics rows, so an analysis.json."""
        has_metrics = exists().where(RunMetricRow.run_id == RunRow.id)
        query = (
            select(RunRow)
            .where(RunRow.experiment == name)
            .where(RunRow.status == RunStatus.SUCCEEDED.value)
            .where(has_metrics)
            .order_by(RunRow.id)
        )
        with self._sessions() as session:
            return list(session.scalars(query))

    def count_analyses(self, name: str) -> int:
        query = select(func.count()).where(ExperimentAnalysisRow.experiment == name)
        with self._sessions() as session:
            return int(session.scalar(query) or 0)

    def delete_experiment(self, name: str, session_ids: list[str]) -> bool:
        """The last step of a user's experiment delete, once its runs are gone: its
        finished tuning sessions and its row, whose analyses cascade; one transaction."""
        with self._sessions.begin() as session:
            if session_ids:
                session.execute(
                    delete(TuningSessionRow).where(TuningSessionRow.id.in_(session_ids))
                )
            row = session.get(ExperimentRow, name)
            if row is None:
                return False
            session.delete(row)
            return True

    def analyses_including(self, run_id: str) -> list[ExperimentAnalysisRow]:
        """The experiment comparisons that included a run (their run_ids)."""
        with self._sessions() as session:
            query = select(ExperimentAnalysisRow).where(
                ExperimentAnalysisRow.run_ids.contains([run_id])
            )
            return list(session.scalars(query))

    def insert_analysis(self, analysis: ExperimentAnalysisRow) -> None:
        with self._sessions.begin() as session:
            session.add(analysis)

    def latest_analysis(self, name: str) -> ExperimentAnalysisRow | None:
        query = (
            select(ExperimentAnalysisRow)
            .where(ExperimentAnalysisRow.experiment == name)
            .order_by(ExperimentAnalysisRow.id.desc())
            .limit(1)
        )
        with self._sessions() as session:
            return session.scalars(query).first()

    def insert_scenario(self, scenario: ScenarioRow) -> None:
        with self._sessions.begin() as session:
            session.add(scenario)

    def get_scenario(self, scenario_id: str) -> ScenarioRow | None:
        with self._sessions() as session:
            return session.get(ScenarioRow, scenario_id)

    def list_scenarios(self) -> list[ScenarioRow]:
        with self._sessions() as session:
            return list(session.scalars(select(ScenarioRow).order_by(ScenarioRow.id.desc())))

    def delete_scenario(self, scenario_id: str) -> bool:
        with self._sessions.begin() as session:
            row = session.get(ScenarioRow, scenario_id)
            if row is None:
                return False
            session.delete(row)
            return True
