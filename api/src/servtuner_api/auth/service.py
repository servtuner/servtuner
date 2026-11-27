"""Local accounts and sessions. The first-run setup is ``setup.py`` and
forward-header accounts are ``forward_header.py``; both hang off ``AuthService``.

``AuthService`` owns the store and the guard; ``app.state.auth`` holds it.
"""

import hashlib
import logging
import secrets
from collections.abc import Callable
from dataclasses import dataclass
from datetime import datetime, timedelta

from fastapi import Request, status

from servtuner_api.auth.forward_header import ForwardHeaderAccounts, ForwardHeaderAuthenticator
from servtuner_api.auth.guard import Authenticator, AuthGuard, refusal
from servtuner_api.auth.passwords import DUMMY_HASH, hash_password, needs_rehash, verify_password
from servtuner_api.auth.principal import AuthMethod, Principal, Role, user_principal
from servtuner_api.auth.setup import FirstAdminSetup
from servtuner_api.auth.store import AuditEntry, AuthStore
from servtuner_api.auth.validation import check_password
from servtuner_api.database import UserRow, UserSessionRow
from servtuner_api.records.base import utc_now
from servtuner_api.settings.models import AuthMode, AuthSettings, LoginMethod

logger = logging.getLogger(__name__)

SESSION_TOKEN_BYTES = 32
# last_seen_at is written at most this often, to keep reads from turning into writes.
SESSION_TOUCH_SECONDS = 60
USER_AGENT_MAX_LENGTH = 256
INVALID_LOGIN = "Invalid username or password"
LOCKOUT_KEY_PREFIX = "user:"


def hash_session_token(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


@dataclass(frozen=True)
class NewSession:
    token: str
    user: UserRow
    max_age_seconds: int


class SessionCookieAuthenticator:
    """A local account's session cookie: refused when it ends idle, at its absolute end, or
    when the user is disabled."""

    method = AuthMethod.SESSION

    def __init__(self, service: AuthService) -> None:
        self._service = service

    def authenticate(self, request: Request) -> Principal | None:
        token = request.cookies.get(self._service.guard.cookie_name)
        if not token:
            return None
        return self._service.principal_for_session(token)


class AuthService:
    def __init__(
        self,
        settings: AuthSettings,
        store: AuthStore | None,
        clock: Callable[[], datetime] = utc_now,
    ) -> None:
        self.settings = settings
        self.store = store
        self._clock = clock
        self._has_admin = False  # once true it stays true: the last admin cannot be removed
        self.forward_header = ForwardHeaderAccounts(self)
        self.setup = FirstAdminSetup(self)
        authenticators: list[Authenticator] = []
        for method in settings.methods if settings.mode is AuthMode.LOCAL else ():
            if method is LoginMethod.LOCAL:
                authenticators.append(SessionCookieAuthenticator(self))
            else:
                authenticators.append(ForwardHeaderAuthenticator(self.forward_header))
        self.guard = AuthGuard(settings, authenticators, self.is_setup_required)

    @classmethod
    def unchecked(cls) -> AuthService:
        """auth.mode none: nothing is checked and no user routes exist."""
        return cls(AuthSettings(mode=AuthMode.NONE, methods=()), None)

    def start(self) -> None:
        if self.store is not None:
            try:
                self.store.sweep_sessions(self._clock())
            except Exception:  # the api starts without a database; logins sweep later
                logger.exception("sweeping expired sessions failed")

    def accounts(self) -> AuthStore:
        """The store, or 404 under auth.mode none, where no user routes exist."""
        if self.store is None or self.settings.mode is AuthMode.NONE:
            raise refusal(
                status.HTTP_404_NOT_FOUND, "auth_off", "user auth is off (auth.mode none)"
            )
        return self.store

    def is_setup_required(self) -> bool:
        if self._has_admin or self.store is None:
            return False
        self._has_admin = self.store.has_active_admin()
        return not self._has_admin

    def now(self) -> datetime:
        return self._clock()

    def mark_admin_exists(self) -> None:
        self._has_admin = True

    def audit(self, entry: AuditEntry) -> None:
        if self.store is not None:
            self.store.audit(entry, self._clock())

    # Sessions

    def principal_for_session(self, token: str) -> Principal:
        store = self.accounts()
        token_hash = hash_session_token(token)
        found = store.session_with_user(token_hash)
        now = self._clock()
        if found is None:
            raise refusal(status.HTTP_401_UNAUTHORIZED, "session_ended", "sign in again")
        session, user = found
        if session.expires_at <= now or session.idle_expires_at <= now:
            store.delete_session(token_hash)
            raise refusal(status.HTTP_401_UNAUTHORIZED, "session_ended", "sign in again")
        if user.disabled_at is not None:
            raise refusal(status.HTTP_401_UNAUTHORIZED, "user_disabled", "this user is disabled")
        if (now - session.last_seen_at).total_seconds() >= SESSION_TOUCH_SECONDS:
            idle_end = min(
                now + timedelta(seconds=self.settings.session_idle_seconds), session.expires_at
            )
            store.touch_session(token_hash, now, idle_end)
        return user_principal(
            user.id,
            user.username,
            Role(user.role),
            AuthMethod.SESSION,
            session_id=token_hash,
            must_change_password=user.must_change_password,
        )

    def open_session(self, user: UserRow, request: Request) -> NewSession:
        store = self.accounts()
        token = secrets.token_urlsafe(SESSION_TOKEN_BYTES)
        now = self._clock()
        absolute = timedelta(seconds=self.settings.session_absolute_seconds)
        idle = timedelta(seconds=self.settings.session_idle_seconds)
        store.insert_session(
            UserSessionRow(
                token_hash=hash_session_token(token),
                user_id=user.id,
                auth_method=AuthMethod.SESSION.value,
                created_at=now,
                last_seen_at=now,
                idle_expires_at=min(now + idle, now + absolute),
                expires_at=now + absolute,
                user_agent=(request.headers.get("user-agent") or "")[:USER_AGENT_MAX_LENGTH]
                or None,
            )
        )
        return NewSession(token, user, self.settings.session_absolute_seconds)

    # Forward header

    # Login and logout

    def require_local(self) -> AuthStore:
        store = self.accounts()
        if not self.settings.allows(LoginMethod.LOCAL):
            raise refusal(
                status.HTTP_404_NOT_FOUND,
                "local_login_off",
                "local accounts are not enabled; sign in through the proxy",
            )
        return store

    def login(self, username: str, password: str, request: Request) -> NewSession:
        store = self.require_local()
        if self.is_setup_required():
            raise refusal(status.HTTP_401_UNAUTHORIZED, "setup_required", "create an admin first")
        now = self._clock()
        key = LOCKOUT_KEY_PREFIX + username.lower()
        locked_until = store.locked_until(key, now)
        if locked_until is not None:
            raise _locked(locked_until, now)
        user = store.user_by_username(username)
        usable = user is not None and user.disabled_at is None and user.password_hash is not None
        stored = user.password_hash if usable and user and user.password_hash else DUMMY_HASH
        if not (verify_password(password, stored) and usable and user is not None):
            self.audit(
                AuditEntry(
                    "login",
                    "failure",
                    AuthMethod.SESSION,
                    user_id=user.id if user else None,
                    subject=username[:USER_AGENT_MAX_LENGTH],
                )
            )
            locked_until = store.record_failure(
                key,
                now,
                self.settings.lockout_max_failures,
                timedelta(seconds=self.settings.lockout_window_seconds),
            )
            if locked_until is not None:  # later attempts get 429 until it lifts
                self.audit(AuditEntry("lockout", "refused", AuthMethod.SESSION, subject=username))
            raise refusal(status.HTTP_401_UNAUTHORIZED, "invalid_credentials", INVALID_LOGIN)
        store.clear_failures(key)
        if needs_rehash(stored):
            store.update_user(user.id, password_hash=hash_password(password))
        store.sweep_sessions(now)
        self.audit(AuditEntry("login", "success", AuthMethod.SESSION, user_id=user.id))
        return self.open_session(user, request)

    def logout(self, request: Request) -> None:
        token = request.cookies.get(self.guard.cookie_name)
        if self.store is None or not token:
            return
        token_hash = hash_session_token(token)
        found = self.store.session_with_user(token_hash)
        if found is not None:
            self.store.delete_session(token_hash)
            self.audit(AuditEntry("logout", "success", AuthMethod.SESSION, user_id=found[1].id))

    # Setup

    # A user's own account

    def change_password(self, principal: Principal, current: str, new: str) -> None:
        store = self.require_local()
        assert principal.user_id is not None  # the guard admits only signed-in users here
        user = store.user(principal.user_id)
        if user is None or user.password_hash is None:
            raise refusal(
                status.HTTP_409_CONFLICT, "no_password", "this user signs in through the proxy"
            )
        if not verify_password(current, user.password_hash):
            self.audit(AuditEntry("password_change", "failure", principal.auth_method, user.id))
            raise refusal(
                status.HTTP_403_FORBIDDEN, "wrong_password", "the current password is wrong"
            )
        check_password(new)
        store.update_user(
            user.id,
            password_hash=hash_password(new),
            must_change_password=False,
            password_changed_at=self._clock(),
        )
        store.delete_user_sessions(user.id, keep_token_hash=principal.session_id)
        self.audit(AuditEntry("password_change", "success", principal.auth_method, user.id))

    def sessions_of(self, principal: Principal) -> list[UserSessionRow]:
        assert principal.user_id is not None
        return self.accounts().list_user_sessions(principal.user_id, self._clock())

    def revoke_session(self, principal: Principal, token_hash: str) -> None:
        assert principal.user_id is not None
        if not self.accounts().delete_session(token_hash, principal.user_id):
            raise refusal(status.HTTP_404_NOT_FOUND, "no_session", "no such session")
        self.audit(
            AuditEntry("session_revoke", "success", principal.auth_method, principal.user_id)
        )

    def revoke_other_sessions(self, principal: Principal) -> int:
        """Sign out everywhere else: every session of the user but this one."""
        assert principal.user_id is not None
        count = self.accounts().delete_user_sessions(
            principal.user_id, keep_token_hash=principal.session_id
        )
        self.audit(
            AuditEntry(
                "session_revoke",
                "success",
                principal.auth_method,
                principal.user_id,
                detail={"count": count},
            )
        )
        return count


TEMPORARY_PASSWORD_BYTES = 18  # 24 URL-safe characters, above the 12-character minimum


def issue_temporary_password(store: AuthStore, user_id: str, now: datetime) -> str:
    """An admin's or the recovery CLI's reset: a random password the user must change, and
    every session of theirs ended."""
    password = secrets.token_urlsafe(TEMPORARY_PASSWORD_BYTES)
    store.update_user(
        user_id,
        password_hash=hash_password(password),
        must_change_password=True,
        password_changed_at=now,
    )
    store.delete_user_sessions(user_id)
    return password


def _locked(until: datetime, now: datetime) -> Exception:
    seconds = max(1, int((until - now).total_seconds()))
    error = refusal(
        status.HTTP_429_TOO_MANY_REQUESTS,
        "locked",
        f"too many failed logins; try again in {seconds} s",
    )
    error.headers = {"Retry-After": str(seconds)}
    return error
