"""Postgres: the tables, the connection URL and migrations.

Table shapes follow api/CLAUDE.md. ``run_metrics`` is long-format so
comparisons need no schema changes. The submitted RunSpec is kept as bytes
(``spec_raw``) because RunSpecs are stored exactly as submitted.
"""

from datetime import datetime
from pathlib import Path
from typing import Any

from alembic import command
from alembic.config import Config
from sqlalchemy import (
    URL,
    BigInteger,
    Boolean,
    DateTime,
    Float,
    ForeignKey,
    Index,
    Integer,
    LargeBinary,
    String,
    UniqueConstraint,
    create_engine,
    event,
    false,
    text,
)
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.engine import Engine
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column, sessionmaker

from servtuner_api.auth.attribution import acting_user_id
from servtuner_api.settings.models import DatabaseSettings, DatabaseSslMode, Hop, TlsSettings

DRIVER = "postgresql+psycopg"
MIGRATIONS_DIR = Path(__file__).parent / "migrations"
# Recycling connections makes a rotated client certificate take effect: libpq
# reads the cert files each time it connects.
CONNECTION_RECYCLE_SECONDS = 300


class Base(DeclarativeBase):
    pass


@event.listens_for(Base, "before_insert", propagate=True)
def _stamp_created_by(_mapper: object, _connection: object, row: object) -> None:
    """A row a user's request inserts records who; see auth/attribution.py."""
    if hasattr(row, "created_by") and row.created_by is None:
        row.created_by = acting_user_id.get()  # type: ignore[attr-defined]


class RunRow(Base):
    __tablename__ = "runs"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String(63))
    experiment: Mapped[str | None] = mapped_column(ForeignKey("experiments.name"), index=True)
    status: Mapped[str] = mapped_column(String(16), index=True)
    phase: Mapped[str | None] = mapped_column(String)
    target_type: Mapped[str] = mapped_column(String(32), index=True)
    knobs: Mapped[dict[str, Any]] = mapped_column(JSONB)
    labels: Mapped[dict[str, str]] = mapped_column(JSONB)
    spec_raw: Mapped[bytes] = mapped_column(LargeBinary)
    spec_hash: Mapped[str] = mapped_column(String(64), index=True)
    bundle_store: Mapped[str] = mapped_column(String(63))
    bundle_key: Mapped[str] = mapped_column(String)
    # The run token's hash, set when a job is launched; None before that.
    token_hash: Mapped[str | None] = mapped_column(String(64), nullable=True)
    token_expires_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    exit_code: Mapped[int | None] = mapped_column(Integer)
    failure_message: Mapped[str | None] = mapped_column(String)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    started_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))


class RunEventRow(Base):
    __tablename__ = "run_events"
    __table_args__ = (UniqueConstraint("run_id", "sequence"),)

    id: Mapped[int] = mapped_column(primary_key=True, autoincrement=True)
    run_id: Mapped[str] = mapped_column(ForeignKey("runs.id", ondelete="CASCADE"), index=True)
    sequence: Mapped[int] = mapped_column(Integer)
    time: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    step: Mapped[str] = mapped_column(String(16))
    state: Mapped[str] = mapped_column(String(16))
    phase: Mapped[str | None] = mapped_column(String)
    message: Mapped[str | None] = mapped_column(String)


class ExperimentRow(Base):
    __tablename__ = "experiments"

    name: Mapped[str] = mapped_column(String, primary_key=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)


class ExperimentAnalysisRow(Base):
    """One servtuner compare result for an experiment; the newest is the current one."""

    __tablename__ = "experiment_analyses"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    experiment: Mapped[str] = mapped_column(
        ForeignKey("experiments.name", ondelete="CASCADE"), index=True
    )
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    objective: Mapped[str] = mapped_column(String(32))
    run_ids: Mapped[list[str]] = mapped_column(JSONB)
    comparison: Mapped[dict[str, Any]] = mapped_column(JSONB)


class ScenarioRow(Base):
    """A saved scenario: a POST /plans body under a name; the plan is recomputed."""

    __tablename__ = "scenarios"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String)
    note: Mapped[str | None] = mapped_column(String, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    body: Mapped[dict[str, Any]] = mapped_column(JSONB)


class TargetRow(Base):
    """A saved target: a RunSpec target block under a name.

    ``target`` holds the block with the managed secret references filled in;
    ``credentials`` holds, per slot, where its key lives and a masked preview,
    never a value. ``last_check`` is the latest check-target result.
    """

    __tablename__ = "targets"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String(63), unique=True)
    description: Mapped[str | None] = mapped_column(String, nullable=True)
    target_type: Mapped[str] = mapped_column(String(32))
    target: Mapped[dict[str, Any]] = mapped_column(JSONB)
    credentials: Mapped[dict[str, Any]] = mapped_column(JSONB)
    last_check: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    # targets.pricing.TargetPricing; None when the target names no prices.
    pricing: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class ManagedTargetRow(Base):
    """A managed target: a vLLM servtuner deploys itself.

    ``spec`` is the validated ManagedTargetSpec. One managed target per
    namespace, so its objects never share a namespace with another's.
    """

    __tablename__ = "managed_targets"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String(63), unique=True)
    description: Mapped[str | None] = mapped_column(String, nullable=True)
    namespace: Mapped[str] = mapped_column(String(63), unique=True)
    spec: Mapped[dict[str, Any]] = mapped_column(JSONB)
    # The last deploy: {knobs, args, deployed_at}; None before the first.
    deployment: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class HostRow(Base):
    """A host: a machine servtuner runs vLLM on, its hardware and price, recorded once
    for every target on it. ``hardware`` and ``price`` are hosts.HostHardware and HostPrice."""

    __tablename__ = "hosts"

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String(63), unique=True)
    note: Mapped[str | None] = mapped_column(String, nullable=True)
    hardware: Mapped[dict[str, Any]] = mapped_column(JSONB)
    price: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))


class PriceBookRow(Base):
    """The install's price book: one row, ``book`` is price_book.PriceBook."""

    __tablename__ = "price_book"

    id: Mapped[str] = mapped_column(String(16), primary_key=True)
    book: Mapped[dict[str, Any]] = mapped_column(JSONB)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    updated_by: Mapped[str | None] = mapped_column(String(26), nullable=True)


class PriceBookDatasheetRow(Base):
    """The token-price datasheet the operator uploaded to the price book: one row,
    the file byte for byte (LiteLLM's model-price JSON, about 5 MB), served to bench runs."""

    __tablename__ = "price_book_datasheet"

    id: Mapped[str] = mapped_column(String(16), primary_key=True)
    content: Mapped[bytes] = mapped_column(LargeBinary, deferred=True)
    sha256: Mapped[str] = mapped_column(String(64))
    size_bytes: Mapped[int] = mapped_column(Integer)
    entries: Mapped[int] = mapped_column(Integer)
    priced_entries: Mapped[int] = mapped_column(Integer)
    uploaded_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    uploaded_by: Mapped[str | None] = mapped_column(String(26), nullable=True)


class TuningSessionRow(Base):
    """A tuning session: its whole state, so a restarted api resumes from the row."""

    __tablename__ = "tuning_sessions"
    __table_args__ = (
        # The lease: at most one active session, since two would share the GPU they measure.
        Index(
            "one_active_tuning_session",
            text("(true)"),
            unique=True,
            postgresql_where=text("status IN ('running', 'awaiting_approval')"),
        ),
    )

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    managed_target_id: Mapped[str] = mapped_column(String(26))
    status: Mapped[str] = mapped_column(String(32))
    request: Mapped[dict[str, Any]] = mapped_column(JSONB)
    experiment: Mapped[str] = mapped_column(String(63))
    step: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    proposal: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    boot_results: Mapped[list[dict[str, Any]]] = mapped_column(JSONB)
    history: Mapped[list[dict[str, Any]]] = mapped_column(JSONB)
    outcome: Mapped[dict[str, Any] | None] = mapped_column(JSONB, nullable=True)
    runs_started: Mapped[int] = mapped_column(Integer)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)
    updated_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    finished_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class ScheduleRow(Base):
    """A RunSpec launched every ``every_seconds`` from ``start_at``.

    Its runs carry the label ``servtuner.schedule=<id>``. ``last_note`` says why
    the last due slot launched nothing (an overlap or a rejected spec).
    """

    __tablename__ = "schedules"
    __table_args__ = (Index("ix_schedules_due", "is_enabled", "next_run_at"),)

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    name: Mapped[str] = mapped_column(String)
    spec_raw: Mapped[bytes] = mapped_column(LargeBinary)
    every_seconds: Mapped[int] = mapped_column(Integer)
    start_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    next_run_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    is_enabled: Mapped[bool] = mapped_column(Boolean)
    baseline_run_id: Mapped[str | None] = mapped_column(String(26), nullable=True)
    regression_tolerance: Mapped[float] = mapped_column(Float)
    last_run_id: Mapped[str | None] = mapped_column(String(26), nullable=True)
    last_note: Mapped[str | None] = mapped_column(String, nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)


class UserRow(Base):
    """A servtuner user. ``id`` is the only key other tables reference."""

    __tablename__ = "users"
    __table_args__ = (Index("ux_users_username_lower", text("lower(username)"), unique=True),)

    id: Mapped[str] = mapped_column(String(26), primary_key=True)
    username: Mapped[str] = mapped_column(String(128))
    display_name: Mapped[str | None] = mapped_column(String, nullable=True)
    email: Mapped[str | None] = mapped_column(String, nullable=True)
    role: Mapped[str] = mapped_column(String(16))
    # None for a user who signs in only through the proxy.
    password_hash: Mapped[str | None] = mapped_column(String, nullable=True)
    must_change_password: Mapped[bool] = mapped_column(
        Boolean, default=False, server_default=false()
    )
    password_changed_at: Mapped[datetime | None] = mapped_column(
        DateTime(timezone=True), nullable=True
    )
    disabled_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    created_by: Mapped[str | None] = mapped_column(String(26), nullable=True)


class UserIdentityRow(Base):
    """An outside identity that maps to a user: a forwarded subject now, an OIDC subject later."""

    __tablename__ = "user_identities"

    provider: Mapped[str] = mapped_column(String(255), primary_key=True)
    subject: Mapped[str] = mapped_column(String(512), primary_key=True)
    user_id: Mapped[str] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"), index=True)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    last_seen_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class UserSessionRow(Base):
    """A local account's session; only the token's SHA-256 is stored."""

    __tablename__ = "user_sessions"

    token_hash: Mapped[str] = mapped_column(String(64), primary_key=True)
    user_id: Mapped[str] = mapped_column(ForeignKey("users.id", ondelete="CASCADE"), index=True)
    auth_method: Mapped[str] = mapped_column(String(32))
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    last_seen_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    idle_expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    expires_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    user_agent: Mapped[str | None] = mapped_column(String, nullable=True)


class AuditEventRow(Base):
    """A security-relevant event. ``detail`` never holds a secret."""

    __tablename__ = "audit_events"

    id: Mapped[int] = mapped_column(BigInteger, primary_key=True, autoincrement=True)
    at: Mapped[datetime] = mapped_column(DateTime(timezone=True), index=True)
    user_id: Mapped[str | None] = mapped_column(String(26), nullable=True)
    auth_method: Mapped[str] = mapped_column(String(32))
    subject: Mapped[str | None] = mapped_column(String(512), nullable=True)
    action: Mapped[str] = mapped_column(String(64))
    target_kind: Mapped[str | None] = mapped_column(String(32), nullable=True)
    target_id: Mapped[str | None] = mapped_column(String, nullable=True)
    outcome: Mapped[str] = mapped_column(String(16))
    detail: Mapped[dict[str, Any]] = mapped_column(JSONB)


class LoginFailureRow(Base):
    """Failed logins per username in the current window, and the lock they caused."""

    __tablename__ = "login_failures"

    key: Mapped[str] = mapped_column(String(160), primary_key=True)
    failures: Mapped[int] = mapped_column(Integer)
    window_started_at: Mapped[datetime] = mapped_column(DateTime(timezone=True))
    locked_until: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)


class RunMetricRow(Base):
    __tablename__ = "run_metrics"
    __table_args__ = (Index("ix_run_metrics_metric_stat", "metric", "stat"),)

    run_id: Mapped[str] = mapped_column(ForeignKey("runs.id", ondelete="CASCADE"), primary_key=True)
    stage: Mapped[str] = mapped_column(String, primary_key=True)
    metric: Mapped[str] = mapped_column(String, primary_key=True)
    stat: Mapped[str] = mapped_column(String, primary_key=True)
    value: Mapped[float] = mapped_column(Float)


def build_database_url(database: DatabaseSettings, tls: TlsSettings) -> URL:
    """The Postgres hop: ``verify-full``, with the api's client certificate when the hop presents
    one, or ``disable``."""
    query: dict[str, str] = {"sslmode": database.ssl_mode.value}
    if database.ssl_mode is DatabaseSslMode.VERIFY_FULL:
        assert tls.ca_bundle_file  # settings guarantee it
        query["sslrootcert"] = str(tls.ca_bundle_file)
        if tls.presents_cert(Hop.POSTGRES):
            assert tls.cert_file and tls.key_file  # settings guarantee these
            query |= {"sslcert": str(tls.cert_file), "sslkey": str(tls.key_file)}
    return URL.create(
        DRIVER,
        username=database.user,
        password=database.password,
        host=database.host,
        port=database.port,
        database=database.name,
        query=query,
    )


def create_database_engine(url: URL | str) -> Engine:
    return create_engine(url, pool_pre_ping=True, pool_recycle=CONNECTION_RECYCLE_SECONDS)


def create_session_factory(engine: Engine) -> sessionmaker:  # type: ignore[type-arg]
    return sessionmaker(engine, expire_on_commit=False)


def upgrade_database(engine: Engine) -> None:
    """Apply every Alembic migration, on a connection the caller already trusts."""
    config = Config()
    config.set_main_option("script_location", str(MIGRATIONS_DIR))
    with engine.begin() as connection:
        config.attributes["connection"] = connection
        command.upgrade(config, "head")
