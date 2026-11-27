"""Postgres access for users, identities, sessions, lockouts and audit.

Everything auth needs lives here, so several api replicas agree: nothing about auth is cached
per process for longer than one request.
"""

from collections.abc import Sequence
from dataclasses import dataclass, field
from datetime import datetime, timedelta
from typing import Any

from sqlalchemy import delete, func, or_, select, text, update
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session, sessionmaker

from servtuner_api.auth.principal import AuthMethod, Role
from servtuner_api.database import (
    AuditEventRow,
    LoginFailureRow,
    UserIdentityRow,
    UserRow,
    UserSessionRow,
)

# Serialises creating the first admin, so two setup requests at once make one admin.
FIRST_ADMIN_LOCK_KEY = 728_314_001
DEFAULT_AUDIT_PAGE = 100


class UsernameTakenError(ValueError):
    pass


class IdentityTakenError(ValueError):
    pass


class AdminExistsError(RuntimeError):
    pass


class LastAdminError(RuntimeError):
    pass


@dataclass(frozen=True)
class AuditEntry:
    """One audit row to write; ``detail`` never holds a secret."""

    action: str
    outcome: str  # success | refused | failure
    auth_method: AuthMethod
    user_id: str | None = None
    subject: str | None = None
    target_kind: str | None = None
    target_id: str | None = None
    detail: dict[str, Any] = field(default_factory=dict)


class AuthStore:
    def __init__(self, sessions: sessionmaker[Session]) -> None:
        self._sessions = sessions

    # Users

    def has_active_admin(self) -> bool:
        query = select(func.count()).where(
            UserRow.role == Role.ADMIN, UserRow.disabled_at.is_(None)
        )
        with self._sessions() as session:
            return bool(session.scalar(query))

    def count_active_admins(self, session: Session) -> int:
        query = select(func.count()).where(
            UserRow.role == Role.ADMIN, UserRow.disabled_at.is_(None)
        )
        return int(session.scalar(query) or 0)

    def user(self, user_id: str) -> UserRow | None:
        with self._sessions() as session:
            return session.get(UserRow, user_id)

    def user_by_username(self, username: str) -> UserRow | None:
        query = select(UserRow).where(func.lower(UserRow.username) == username.lower())
        with self._sessions() as session:
            return session.scalar(query)

    def list_users(self) -> list[UserRow]:
        with self._sessions() as session:
            return list(session.scalars(select(UserRow).order_by(func.lower(UserRow.username))))

    def insert_user(self, user: UserRow, identities: Sequence[UserIdentityRow] = ()) -> UserRow:
        try:
            with self._sessions.begin() as session:
                session.add(user)
                session.flush()
                session.add_all(identities)
        except IntegrityError as error:
            raise _taken(error) from error
        return user

    def insert_first_admin(
        self, user: UserRow, identities: Sequence[UserIdentityRow] = ()
    ) -> UserRow:
        """The first admin, under a lock: a second setup request at once gets AdminExistsError."""
        try:
            with self._sessions.begin() as session:
                session.execute(
                    text("SELECT pg_advisory_xact_lock(:key)"), {"key": FIRST_ADMIN_LOCK_KEY}
                )
                if self.count_active_admins(session):
                    raise AdminExistsError("an admin exists")
                session.add(user)
                session.flush()
                session.add_all(identities)
        except IntegrityError as error:
            raise _taken(error) from error
        return user

    def update_user(self, user_id: str, **fields: Any) -> UserRow | None:
        with self._sessions.begin() as session:
            user = session.get(UserRow, user_id, with_for_update=True)
            if user is None:
                return None
            for name, value in fields.items():
                setattr(user, name, value)
            return user

    def update_user_keeping_an_admin(self, user_id: str, **fields: Any) -> UserRow | None:
        """Like update_user, but raises LastAdminError rather than leave no active admin."""
        with self._sessions.begin() as session:
            session.execute(
                text("SELECT pg_advisory_xact_lock(:key)"), {"key": FIRST_ADMIN_LOCK_KEY}
            )
            user = session.get(UserRow, user_id, with_for_update=True)
            if user is None:
                return None
            for name, value in fields.items():
                setattr(user, name, value)
            session.flush()
            if self.count_active_admins(session) == 0:
                raise LastAdminError("this would leave servtuner without an active admin")
            return user

    # Identities

    def user_by_identity(self, provider: str, subject: str, now: datetime) -> UserRow | None:
        """The user an outside identity maps to; stamps when it was last seen."""
        with self._sessions.begin() as session:
            identity = session.get(UserIdentityRow, (provider, subject))
            if identity is None:
                return None
            identity.last_seen_at = now
            return session.get(UserRow, identity.user_id)

    def identities_of(self, user_id: str) -> list[UserIdentityRow]:
        query = select(UserIdentityRow).where(UserIdentityRow.user_id == user_id)
        with self._sessions() as session:
            return list(session.scalars(query.order_by(UserIdentityRow.created_at)))

    def add_identity(self, identity: UserIdentityRow) -> None:
        try:
            with self._sessions.begin() as session:
                session.add(identity)
        except IntegrityError as error:
            raise IdentityTakenError(f"{identity.subject!r} already belongs to a user") from error

    def remove_identity(self, user_id: str, provider: str, subject: str) -> bool:
        statement = delete(UserIdentityRow).where(
            UserIdentityRow.user_id == user_id,
            UserIdentityRow.provider == provider,
            UserIdentityRow.subject == subject,
        )
        with self._sessions.begin() as session:
            return session.execute(statement).rowcount > 0  # type: ignore[attr-defined]

    # Sessions

    def insert_session(self, row: UserSessionRow) -> None:
        with self._sessions.begin() as session:
            session.add(row)

    def session_with_user(self, token_hash: str) -> tuple[UserSessionRow, UserRow] | None:
        query = (
            select(UserSessionRow, UserRow)
            .join(UserRow, UserRow.id == UserSessionRow.user_id)
            .where(UserSessionRow.token_hash == token_hash)
        )
        with self._sessions() as session:
            found = session.execute(query).first()
            return (found[0], found[1]) if found else None

    def touch_session(self, token_hash: str, now: datetime, idle_expires_at: datetime) -> None:
        statement = (
            update(UserSessionRow)
            .where(UserSessionRow.token_hash == token_hash)
            .values(last_seen_at=now, idle_expires_at=idle_expires_at)
        )
        with self._sessions.begin() as session:
            session.execute(statement)

    def delete_session(self, token_hash: str, user_id: str | None = None) -> bool:
        statement = delete(UserSessionRow).where(UserSessionRow.token_hash == token_hash)
        if user_id is not None:
            statement = statement.where(UserSessionRow.user_id == user_id)
        with self._sessions.begin() as session:
            return session.execute(statement).rowcount > 0  # type: ignore[attr-defined]

    def delete_user_sessions(self, user_id: str, keep_token_hash: str | None = None) -> int:
        statement = delete(UserSessionRow).where(UserSessionRow.user_id == user_id)
        if keep_token_hash is not None:
            statement = statement.where(UserSessionRow.token_hash != keep_token_hash)
        with self._sessions.begin() as session:
            return int(session.execute(statement).rowcount)  # type: ignore[attr-defined]

    def list_user_sessions(self, user_id: str, now: datetime) -> list[UserSessionRow]:
        query = select(UserSessionRow).where(
            UserSessionRow.user_id == user_id,
            UserSessionRow.expires_at > now,
            UserSessionRow.idle_expires_at > now,
        )
        with self._sessions() as session:
            return list(session.scalars(query.order_by(UserSessionRow.last_seen_at.desc())))

    def sweep_sessions(self, now: datetime) -> int:
        """Delete every expired session row; the guard already refuses them."""
        statement = delete(UserSessionRow).where(
            or_(UserSessionRow.expires_at <= now, UserSessionRow.idle_expires_at <= now)
        )
        with self._sessions.begin() as session:
            return int(session.execute(statement).rowcount)  # type: ignore[attr-defined]

    # Lockouts

    def locked_until(self, key: str, now: datetime) -> datetime | None:
        with self._sessions() as session:
            row = session.get(LoginFailureRow, key)
            if row is None or row.locked_until is None or row.locked_until <= now:
                return None
            return row.locked_until

    def record_failure(
        self, key: str, now: datetime, max_failures: int, window: timedelta
    ) -> datetime | None:
        """Count one failure; returns when the key is now locked until, or None."""
        with self._sessions.begin() as session:
            row = session.get(LoginFailureRow, key, with_for_update=True)
            if row is None:
                row = LoginFailureRow(key=key, failures=0, window_started_at=now)
                session.add(row)
            if row.window_started_at + window <= now:
                row.failures, row.window_started_at, row.locked_until = 0, now, None
            row.failures += 1
            if row.failures >= max_failures:
                row.locked_until = now + window
                row.failures, row.window_started_at = 0, now
                return row.locked_until
            return None

    def clear_failures(self, key: str) -> None:
        with self._sessions.begin() as session:
            session.execute(delete(LoginFailureRow).where(LoginFailureRow.key == key))

    # Audit

    def audit(self, entry: AuditEntry, now: datetime) -> None:
        row = AuditEventRow(
            at=now,
            user_id=entry.user_id,
            auth_method=entry.auth_method.value,
            subject=entry.subject,
            action=entry.action,
            target_kind=entry.target_kind,
            target_id=entry.target_id,
            outcome=entry.outcome,
            detail=entry.detail,
        )
        with self._sessions.begin() as session:
            session.add(row)

    def list_audit(
        self, limit: int = DEFAULT_AUDIT_PAGE, before_id: int | None = None
    ) -> list[AuditEventRow]:
        query = select(AuditEventRow).order_by(AuditEventRow.id.desc()).limit(limit)
        if before_id is not None:
            query = query.where(AuditEventRow.id < before_id)
        with self._sessions() as session:
            return list(session.scalars(query))


def _taken(error: IntegrityError) -> ValueError:
    if "user_identities" in str(error.orig):
        return IdentityTakenError("that identity already belongs to a user")
    return UsernameTakenError("that username is taken")
