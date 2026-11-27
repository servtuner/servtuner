"""The first admin: the setup token, read from a file and never stored, and the
admin made from a username and password or from the subject the proxy forwards."""

from __future__ import annotations

import hmac
import logging
from pathlib import Path
from typing import TYPE_CHECKING

from fastapi import Request, status

from servtuner_api.auth.guard import refusal
from servtuner_api.auth.passwords import hash_password
from servtuner_api.auth.principal import AuthMethod, Principal, Role, user_principal
from servtuner_api.auth.store import AdminExistsError, AuditEntry, UsernameTakenError
from servtuner_api.auth.validation import check_password, check_username
from servtuner_api.database import UserRow
from servtuner_api.ids import new_ulid
from servtuner_api.settings.models import LoginMethod

if TYPE_CHECKING:  # the service builds this
    from servtuner_api.auth.service import AuthService, NewSession

logger = logging.getLogger(__name__)


class FirstAdminSetup:
    """Fails closed: with no setup token configured, no admin can be made."""

    def __init__(self, service: AuthService) -> None:
        self._service = service

    def _setup_token(self) -> str | None:
        path: Path | None = self._service.settings.setup_token_file
        if path is None:
            return None
        try:
            return path.read_text().strip() or None
        except OSError:
            logger.exception("reading the setup token file failed")
            return None

    def check_token(self, presented: str | None) -> None:
        """Fails closed: no token configured means no setup at all."""
        expected = self._setup_token()
        if expected is None:
            raise refusal(
                status.HTTP_403_FORBIDDEN,
                "setup_token_not_configured",
                "no setup token is configured, so no admin can be created",
            )
        if not presented:
            raise refusal(
                status.HTTP_401_UNAUTHORIZED, "missing_setup_token", "send X-Servtuner-Setup-Token"
            )
        if not hmac.compare_digest(presented.encode(), expected.encode()):
            self._service.audit(
                AuditEntry("setup", "refused", AuthMethod.NONE, detail={"reason": "token"})
            )
            raise refusal(status.HTTP_403_FORBIDDEN, "invalid_setup_token", "wrong setup token")

    def local_admin(
        self, token: str | None, username: str, password: str, request: Request
    ) -> NewSession:
        store = self._service.require_local()
        if not self._service.is_setup_required():
            raise _setup_done()
        self.check_token(token)
        check_username(username)
        check_password(password)
        now = self._service.now()
        admin = UserRow(
            id=new_ulid(),
            username=username,
            role=Role.ADMIN.value,
            password_hash=hash_password(password),
            must_change_password=False,
            password_changed_at=now,
            created_at=now,
        )
        try:
            store.insert_first_admin(admin)
        except AdminExistsError as error:
            raise _setup_done() from error
        except UsernameTakenError as error:
            raise refusal(status.HTTP_409_CONFLICT, "username_taken", str(error)) from error
        self._service.mark_admin_exists()
        self._service.audit(AuditEntry("setup", "success", AuthMethod.SESSION, user_id=admin.id))
        return self._service.open_session(admin, request)

    def forwarded_admin(self, token: str | None, request: Request) -> Principal:
        """The first admin is the subject the proxy forwards with the setup request."""
        store = self._service.accounts()
        if not self._service.settings.allows(LoginMethod.FORWARD_HEADER):
            raise refusal(
                status.HTTP_422_UNPROCESSABLE_CONTENT,
                "username_required",
                "forward-header auth is off: give a username and password",
            )
        if not self._service.is_setup_required():
            raise _setup_done()
        self.check_token(token)
        subject = self._service.forward_header.forwarded_subject(request)
        if subject is None:
            raise refusal(
                status.HTTP_400_BAD_REQUEST,
                "missing_forwarded_subject",
                "the setup request did not come through the proxy: no user header",
            )
        admin = self._service.forward_header.new_user(subject, Role.ADMIN, request)
        try:
            store.insert_first_admin(admin, [self._service.forward_header.identity(admin, subject)])
        except AdminExistsError as error:
            raise _setup_done() from error
        self._service.mark_admin_exists()
        self._service.audit(
            AuditEntry(
                "setup", "success", AuthMethod.FORWARD_HEADER, user_id=admin.id, subject=subject
            )
        )
        return user_principal(admin.id, admin.username, Role.ADMIN, AuthMethod.FORWARD_HEADER)


def _setup_done() -> Exception:
    return refusal(status.HTTP_409_CONFLICT, "setup_done", "an admin exists; setup is closed")
