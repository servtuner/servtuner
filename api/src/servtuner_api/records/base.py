"""Run statuses, filters and clocks, and the base every record class shares."""

from dataclasses import dataclass
from datetime import UTC, datetime
from enum import StrEnum

from sqlalchemy.orm import Session, sessionmaker

from servtuner_api.database import RunRow
from servtuner_api.models.generated.events_schema import ReportedState


class RunStatus(StrEnum):
    # A follow-up run waits here until the user confirms the change it tests
    # is applied to the target; then it is pending.
    AWAITING_CONFIG = "awaiting_config"
    PENDING = "pending"
    LAUNCHING = "launching"
    RUNNING = "running"
    COLLECTING = "collecting"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELLED = "cancelled"


FINAL_STATUSES = frozenset({RunStatus.SUCCEEDED, RunStatus.FAILED, RunStatus.CANCELLED})


# Tuning sessions holding the lease (one session at a time).
ACTIVE_SESSION_STATUSES = ("running", "awaiting_approval")


# The only states an event can move a run into. succeeded/failed from bench are
# recorded in the timeline but never end the run: the runner decides that.
EVENT_STATE_TO_STATUS = {
    ReportedState.running: RunStatus.RUNNING,
    ReportedState.collecting: RunStatus.COLLECTING,
}


@dataclass(frozen=True)
class ExperimentListing:
    name: str
    created_at: datetime
    run_count: int
    latest_run_at: datetime | None


@dataclass(frozen=True)
class RunFilters:
    experiment: str | None = None
    target_type: str | None = None
    status: RunStatus | None = None
    label: tuple[str, str] | None = None


def utc_now() -> datetime:
    return datetime.now(UTC)


# Runs a runner may have started and not yet ended. PENDING is included: a run inserted but not
# launched when the api stopped is never launched later, and its runner says so.
UNFINISHED_LAUNCHED = (
    RunStatus.PENDING,
    RunStatus.LAUNCHING,
    RunStatus.RUNNING,
    RunStatus.COLLECTING,
)


# More unfinished runs than this at startup is not a single install's load.
REWATCH_LIMIT = 1000


def is_final(status: str) -> bool:
    return RunStatus(status) in FINAL_STATUSES


class Records:
    """Holds the session factory; each domain's record class adds its queries."""

    def __init__(self, sessions: sessionmaker[Session]) -> None:
        self._sessions = sessions

    @staticmethod
    def _locked(session: Session, run_id: str) -> RunRow:
        run = session.get(RunRow, run_id, with_for_update=True)
        if run is None:
            raise LookupError(f"run {run_id} not found")
        return run
