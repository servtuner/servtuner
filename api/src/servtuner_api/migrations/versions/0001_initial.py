"""The initial schema: every table, index and constraint servtuner's models define.

Revision ID: 0001
Revises:
"""

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision = "0001"
down_revision = None


def upgrade() -> None:
    op.create_table(
        "audit_events",
        sa.Column("id", sa.BigInteger(), autoincrement=True, nullable=False),
        sa.Column("at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("user_id", sa.String(length=26), nullable=True),
        sa.Column("auth_method", sa.String(length=32), nullable=False),
        sa.Column("subject", sa.String(length=512), nullable=True),
        sa.Column("action", sa.String(length=64), nullable=False),
        sa.Column("target_kind", sa.String(length=32), nullable=True),
        sa.Column("target_id", sa.String(), nullable=True),
        sa.Column("outcome", sa.String(length=16), nullable=False),
        sa.Column("detail", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index(op.f("ix_audit_events_at"), "audit_events", ["at"], unique=False)
    op.create_table(
        "experiments",
        sa.Column("name", sa.String(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.PrimaryKeyConstraint("name"),
    )
    op.create_table(
        "hosts",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(length=63), nullable=False),
        sa.Column("note", sa.String(), nullable=True),
        sa.Column("hardware", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("price", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("name"),
    )
    op.create_table(
        "login_failures",
        sa.Column("key", sa.String(length=160), nullable=False),
        sa.Column("failures", sa.Integer(), nullable=False),
        sa.Column("window_started_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("locked_until", sa.DateTime(timezone=True), nullable=True),
        sa.PrimaryKeyConstraint("key"),
    )
    op.create_table(
        "managed_targets",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(length=63), nullable=False),
        sa.Column("description", sa.String(), nullable=True),
        sa.Column("namespace", sa.String(length=63), nullable=False),
        sa.Column("spec", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("deployment", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("name"),
        sa.UniqueConstraint("namespace"),
    )
    op.create_table(
        "price_book",
        sa.Column("id", sa.String(length=16), nullable=False),
        sa.Column("book", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("updated_by", sa.String(length=26), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_table(
        "price_book_datasheet",
        sa.Column("id", sa.String(length=16), nullable=False),
        sa.Column("content", sa.LargeBinary(), nullable=False),
        sa.Column("sha256", sa.String(length=64), nullable=False),
        sa.Column("size_bytes", sa.Integer(), nullable=False),
        sa.Column("entries", sa.Integer(), nullable=False),
        sa.Column("priced_entries", sa.Integer(), nullable=False),
        sa.Column("uploaded_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("uploaded_by", sa.String(length=26), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_table(
        "scenarios",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(), nullable=False),
        sa.Column("note", sa.String(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("body", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_table(
        "schedules",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(), nullable=False),
        sa.Column("spec_raw", sa.LargeBinary(), nullable=False),
        sa.Column("every_seconds", sa.Integer(), nullable=False),
        sa.Column("start_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("next_run_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("is_enabled", sa.Boolean(), nullable=False),
        sa.Column("baseline_run_id", sa.String(length=26), nullable=True),
        sa.Column("regression_tolerance", sa.Float(), nullable=False),
        sa.Column("last_run_id", sa.String(length=26), nullable=True),
        sa.Column("last_note", sa.String(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index("ix_schedules_due", "schedules", ["is_enabled", "next_run_at"], unique=False)
    op.create_table(
        "targets",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(length=63), nullable=False),
        sa.Column("description", sa.String(), nullable=True),
        sa.Column("target_type", sa.String(length=32), nullable=False),
        sa.Column("target", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("credentials", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("last_check", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("pricing", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("name"),
    )
    op.create_table(
        "tuning_sessions",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("managed_target_id", sa.String(length=26), nullable=False),
        sa.Column("status", sa.String(length=32), nullable=False),
        sa.Column("request", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("experiment", sa.String(length=63), nullable=False),
        sa.Column("step", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("proposal", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("boot_results", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("history", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("outcome", postgresql.JSONB(astext_type=sa.Text()), nullable=True),
        sa.Column("runs_started", sa.Integer(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("updated_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index(
        "one_active_tuning_session",
        "tuning_sessions",
        [sa.literal_column("(true)")],
        unique=True,
        postgresql_where=sa.text("status IN ('running', 'awaiting_approval')"),
    )
    op.create_table(
        "users",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("username", sa.String(length=128), nullable=False),
        sa.Column("display_name", sa.String(), nullable=True),
        sa.Column("email", sa.String(), nullable=True),
        sa.Column("role", sa.String(length=16), nullable=False),
        sa.Column("password_hash", sa.String(), nullable=True),
        sa.Column(
            "must_change_password", sa.Boolean(), server_default=sa.text("false"), nullable=False
        ),
        sa.Column("password_changed_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("disabled_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index(
        "ux_users_username_lower", "users", [sa.literal_column("lower(username)")], unique=True
    )
    op.create_table(
        "experiment_analyses",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("experiment", sa.String(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("objective", sa.String(length=32), nullable=False),
        sa.Column("run_ids", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("comparison", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.ForeignKeyConstraint(["experiment"], ["experiments.name"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index(
        op.f("ix_experiment_analyses_experiment"),
        "experiment_analyses",
        ["experiment"],
        unique=False,
    )
    op.create_table(
        "runs",
        sa.Column("id", sa.String(length=26), nullable=False),
        sa.Column("name", sa.String(length=63), nullable=False),
        sa.Column("experiment", sa.String(), nullable=True),
        sa.Column("status", sa.String(length=16), nullable=False),
        sa.Column("phase", sa.String(), nullable=True),
        sa.Column("target_type", sa.String(length=32), nullable=False),
        sa.Column("knobs", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("labels", postgresql.JSONB(astext_type=sa.Text()), nullable=False),
        sa.Column("spec_raw", sa.LargeBinary(), nullable=False),
        sa.Column("spec_hash", sa.String(length=64), nullable=False),
        sa.Column("bundle_store", sa.String(length=63), nullable=False),
        sa.Column("bundle_key", sa.String(), nullable=False),
        sa.Column("token_hash", sa.String(length=64), nullable=True),
        sa.Column("token_expires_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("created_by", sa.String(length=26), nullable=True),
        sa.Column("exit_code", sa.Integer(), nullable=True),
        sa.Column("failure_message", sa.String(), nullable=True),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("started_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(
            ["experiment"],
            ["experiments.name"],
        ),
        sa.PrimaryKeyConstraint("id"),
    )
    op.create_index(op.f("ix_runs_experiment"), "runs", ["experiment"], unique=False)
    op.create_index(op.f("ix_runs_spec_hash"), "runs", ["spec_hash"], unique=False)
    op.create_index(op.f("ix_runs_status"), "runs", ["status"], unique=False)
    op.create_index(op.f("ix_runs_target_type"), "runs", ["target_type"], unique=False)
    op.create_table(
        "user_identities",
        sa.Column("provider", sa.String(length=255), nullable=False),
        sa.Column("subject", sa.String(length=512), nullable=False),
        sa.Column("user_id", sa.String(length=26), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("last_seen_at", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("provider", "subject"),
    )
    op.create_index(
        op.f("ix_user_identities_user_id"), "user_identities", ["user_id"], unique=False
    )
    op.create_table(
        "user_sessions",
        sa.Column("token_hash", sa.String(length=64), nullable=False),
        sa.Column("user_id", sa.String(length=26), nullable=False),
        sa.Column("auth_method", sa.String(length=32), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("last_seen_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("idle_expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("user_agent", sa.String(), nullable=True),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("token_hash"),
    )
    op.create_index(
        op.f("ix_user_sessions_expires_at"), "user_sessions", ["expires_at"], unique=False
    )
    op.create_index(op.f("ix_user_sessions_user_id"), "user_sessions", ["user_id"], unique=False)
    op.create_table(
        "run_events",
        sa.Column("id", sa.Integer(), autoincrement=True, nullable=False),
        sa.Column("run_id", sa.String(length=26), nullable=False),
        sa.Column("sequence", sa.Integer(), nullable=False),
        sa.Column("time", sa.DateTime(timezone=True), nullable=False),
        sa.Column("step", sa.String(length=16), nullable=False),
        sa.Column("state", sa.String(length=16), nullable=False),
        sa.Column("phase", sa.String(), nullable=True),
        sa.Column("message", sa.String(), nullable=True),
        sa.ForeignKeyConstraint(["run_id"], ["runs.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("run_id", "sequence"),
    )
    op.create_index(op.f("ix_run_events_run_id"), "run_events", ["run_id"], unique=False)
    op.create_table(
        "run_metrics",
        sa.Column("run_id", sa.String(length=26), nullable=False),
        sa.Column("stage", sa.String(), nullable=False),
        sa.Column("metric", sa.String(), nullable=False),
        sa.Column("stat", sa.String(), nullable=False),
        sa.Column("value", sa.Float(), nullable=False),
        sa.ForeignKeyConstraint(["run_id"], ["runs.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("run_id", "stage", "metric", "stat"),
    )
    op.create_index("ix_run_metrics_metric_stat", "run_metrics", ["metric", "stat"], unique=False)


def downgrade() -> None:
    op.drop_index("ix_run_metrics_metric_stat", table_name="run_metrics")
    op.drop_table("run_metrics")
    op.drop_index(op.f("ix_run_events_run_id"), table_name="run_events")
    op.drop_table("run_events")
    op.drop_index(op.f("ix_user_sessions_user_id"), table_name="user_sessions")
    op.drop_index(op.f("ix_user_sessions_expires_at"), table_name="user_sessions")
    op.drop_table("user_sessions")
    op.drop_index(op.f("ix_user_identities_user_id"), table_name="user_identities")
    op.drop_table("user_identities")
    op.drop_index(op.f("ix_runs_target_type"), table_name="runs")
    op.drop_index(op.f("ix_runs_status"), table_name="runs")
    op.drop_index(op.f("ix_runs_spec_hash"), table_name="runs")
    op.drop_index(op.f("ix_runs_experiment"), table_name="runs")
    op.drop_table("runs")
    op.drop_index(op.f("ix_experiment_analyses_experiment"), table_name="experiment_analyses")
    op.drop_table("experiment_analyses")
    op.drop_index("ux_users_username_lower", table_name="users")
    op.drop_table("users")
    op.drop_index(
        "one_active_tuning_session",
        table_name="tuning_sessions",
        postgresql_where=sa.text("status IN ('running', 'awaiting_approval')"),
    )
    op.drop_table("tuning_sessions")
    op.drop_table("targets")
    op.drop_index("ix_schedules_due", table_name="schedules")
    op.drop_table("schedules")
    op.drop_table("scenarios")
    op.drop_table("price_book_datasheet")
    op.drop_table("price_book")
    op.drop_table("managed_targets")
    op.drop_table("login_failures")
    op.drop_table("hosts")
    op.drop_table("experiments")
    op.drop_index(op.f("ix_audit_events_at"), table_name="audit_events")
    op.drop_table("audit_events")
