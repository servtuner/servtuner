"""Forward-header accounts: the subject a trusted proxy names, the
user it maps to, and making one on first sight."""

from __future__ import annotations

from typing import TYPE_CHECKING

from fastapi import Request, status

from servtuner_api.auth.guard import refusal
from servtuner_api.auth.principal import AuthMethod, Principal, Role, user_principal
from servtuner_api.auth.store import AuditEntry, IdentityTakenError, UsernameTakenError
from servtuner_api.auth.validation import USERNAME_CHARACTERS, USERNAME_MAX_LENGTH
from servtuner_api.database import UserIdentityRow, UserRow
from servtuner_api.ids import new_ulid
from servtuner_api.settings.models import Provisioning

if TYPE_CHECKING:  # the service builds this
    from servtuner_api.auth.service import AuthService

FORWARD_HEADER_PROVIDER = "forward_header"  # user_identities.provider for forwarded subjects
SUBJECT_MAX_LENGTH = 512
PROFILE_FIELD_MAX_LENGTH = 256


class ForwardHeaderAuthenticator:
    """The user the trusted proxy names. Trust is implicit: the platform keeps other
    callers away from the api. A header sent twice is refused, since the first copy could be the
    client's own that the proxy failed to overwrite."""

    method = AuthMethod.FORWARD_HEADER

    def __init__(self, accounts: ForwardHeaderAccounts) -> None:
        self._accounts = accounts

    def authenticate(self, request: Request) -> Principal | None:
        subject = self._accounts.forwarded_subject(request)
        if subject is None:
            return None
        return self._accounts.principal_for_subject(subject, request)


class ForwardHeaderAccounts:
    """Subjects, the users they map to, and provisioning."""

    def __init__(self, service: AuthService) -> None:
        self._service = service

    def forwarded_subject(self, request: Request) -> str | None:
        """The subject the proxy sent, checked; None when the header is absent."""
        values = request.headers.getlist(self._service.settings.forward_header.user_header)
        if not values:
            return None
        subject = values[0].strip()
        if (
            len(values) > 1
            or not 1 <= len(subject) <= SUBJECT_MAX_LENGTH
            or not (subject.isprintable())
        ):
            raise refusal(
                status.HTTP_400_BAD_REQUEST,
                "invalid_forwarded_subject",
                f"the {self._service.settings.forward_header.user_header} header must be sent "
                "once, "
                f"with 1 to {SUBJECT_MAX_LENGTH} printable characters",
            )
        return subject

    def principal_for_subject(self, subject: str, request: Request) -> Principal:
        store = self._service.accounts()
        if self._service.is_setup_required():
            raise refusal(
                status.HTTP_401_UNAUTHORIZED,
                "setup_required",
                "servtuner has no admin yet: create one with the setup token",
                subject=subject,
            )
        now = self._service.now()
        user = store.user_by_identity(FORWARD_HEADER_PROVIDER, subject, now)
        if (
            user is None
            and self._service.settings.forward_header.provisioning is Provisioning.VIEWER
        ):
            user = self._provision(subject, request)
        if user is None:
            self._service.audit(
                AuditEntry("forward_header", "refused", AuthMethod.FORWARD_HEADER, subject=subject)
            )
            raise refusal(
                status.HTTP_403_FORBIDDEN,
                "unknown_identity",
                f"your identity {subject!r} has no servtuner account; ask an admin to add it",
                subject=subject,
            )
        if user.disabled_at is not None:
            raise refusal(status.HTTP_401_UNAUTHORIZED, "user_disabled", "this user is disabled")
        return user_principal(user.id, user.username, Role(user.role), AuthMethod.FORWARD_HEADER)

    def _profile(self, request: Request, header: str | None) -> str | None:
        if header is None:
            return None
        value = (request.headers.get(header) or "").strip()[:PROFILE_FIELD_MAX_LENGTH]
        return value if value.isprintable() and value else None

    def new_user(self, subject: str, role: Role, request: Request) -> UserRow:
        settings = self._service.settings.forward_header
        return UserRow(
            id=new_ulid(),
            username=self._free_username(subject),
            display_name=self._profile(request, settings.display_name_header),
            email=self._profile(request, settings.email_header),
            role=role.value,
            must_change_password=False,
            created_at=self._service.now(),
        )

    def identity(self, user: UserRow, subject: str) -> UserIdentityRow:
        return UserIdentityRow(
            provider=FORWARD_HEADER_PROVIDER,
            subject=subject,
            user_id=user.id,
            created_at=self._service.now(),
            last_seen_at=self._service.now(),
        )

    def _provision(self, subject: str, request: Request) -> UserRow | None:
        """Provisioning as viewer: a new viewer on first sight. A concurrent first request may win
        the race; then its user is the one."""
        store = self._service.accounts()
        user = self.new_user(subject, Role.VIEWER, request)
        try:
            store.insert_user(user, [self.identity(user, subject)])
        except IdentityTakenError, UsernameTakenError:
            return store.user_by_identity(FORWARD_HEADER_PROVIDER, subject, self._service.now())
        self._service.audit(
            AuditEntry(
                "provision",
                "success",
                AuthMethod.FORWARD_HEADER,
                user_id=user.id,
                subject=subject,
                target_kind="user",
                target_id=user.id,
            )
        )
        return user

    def _free_username(self, subject: str) -> str:
        """A username from the subject: disallowed characters become '-', made unique."""
        store = self._service.accounts()
        base = "".join(c if c in USERNAME_CHARACTERS else "-" for c in subject).strip("-")
        base = (base or "user")[: USERNAME_MAX_LENGTH - 4]
        candidate, suffix = base, 2
        while store.user_by_username(candidate) is not None:
            candidate, suffix = f"{base}-{suffix}", suffix + 1
        return candidate
