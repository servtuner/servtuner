"""The admin's users page: users, roles, identities, resets, audit.

Users are never deleted, only disabled, so ``created_by`` and audit rows always resolve. No
change may leave servtuner without an active admin.
"""

from datetime import datetime
from typing import Annotated

from fastapi import APIRouter, Query, Request, status
from pydantic import BaseModel, Field

from servtuner_api.auth.forward_header import FORWARD_HEADER_PROVIDER, SUBJECT_MAX_LENGTH
from servtuner_api.auth.guard import refusal
from servtuner_api.auth.passwords import hash_password
from servtuner_api.auth.principal import Principal, Role
from servtuner_api.auth.service import AuthService, issue_temporary_password
from servtuner_api.auth.store import (
    AuditEntry,
    AuthStore,
    IdentityTakenError,
    LastAdminError,
    UsernameTakenError,
)
from servtuner_api.auth.validation import check_password, check_username
from servtuner_api.database import AuditEventRow, UserIdentityRow, UserRow
from servtuner_api.ids import ULID_PATTERN, new_ulid
from servtuner_api.records.base import utc_now

MAX_AUDIT_PAGE = 500


class IdentityView(BaseModel):
    provider: str
    subject: str
    created_at: datetime
    last_seen_at: datetime | None


class UserAdminView(BaseModel):
    id: str
    username: str
    display_name: str | None
    email: str | None
    role: Role
    has_password: bool
    must_change_password: bool
    is_disabled: bool
    created_at: datetime
    identities: list[IdentityView]


class UserCreate(BaseModel):
    """A local user gets a password they must change at first sign-in; a proxy user gets
    forwarded subjects instead (or both, for break-glass)."""

    username: str = Field(min_length=1, max_length=128)
    role: Role
    password: str | None = Field(default=None, max_length=256)
    display_name: str | None = Field(default=None, max_length=256)
    email: str | None = Field(default=None, max_length=256)
    subjects: list[str] = Field(default_factory=list)


class UserUpdate(BaseModel):
    role: Role | None = None
    display_name: str | None = Field(default=None, max_length=256)
    email: str | None = Field(default=None, max_length=256)
    is_disabled: bool | None = None


class IdentityLink(BaseModel):
    subject: str = Field(min_length=1, max_length=SUBJECT_MAX_LENGTH)


class TemporaryPassword(BaseModel):
    """Shown once; the user must change it at their next sign-in."""

    password: str


class AuditEventView(BaseModel):
    id: int
    at: datetime
    user_id: str | None
    auth_method: str
    subject: str | None
    action: str
    target_kind: str | None
    target_id: str | None
    outcome: str
    detail: dict[str, object]

    @classmethod
    def of(cls, row: AuditEventRow) -> AuditEventView:
        return cls(
            id=row.id,
            at=row.at,
            user_id=row.user_id,
            auth_method=row.auth_method,
            subject=row.subject,
            action=row.action,
            target_kind=row.target_kind,
            target_id=row.target_id,
            outcome=row.outcome,
            detail=row.detail,
        )


def _view(store: AuthStore, user: UserRow) -> UserAdminView:
    return UserAdminView(
        id=user.id,
        username=user.username,
        display_name=user.display_name,
        email=user.email,
        role=Role(user.role),
        has_password=user.password_hash is not None,
        must_change_password=user.must_change_password,
        is_disabled=user.disabled_at is not None,
        created_at=user.created_at,
        identities=[
            IdentityView(
                provider=identity.provider,
                subject=identity.subject,
                created_at=identity.created_at,
                last_seen_at=identity.last_seen_at,
            )
            for identity in store.identities_of(user.id)
        ],
    )


def _context(request: Request) -> tuple[AuthService, AuthStore, Principal]:
    auth: AuthService = request.app.state.auth
    principal: Principal = request.state.principal
    return auth, auth.accounts(), principal


def _user_or_404(store: AuthStore, user_id: str) -> UserRow:
    user = store.user(user_id) if ULID_PATTERN.match(user_id) else None
    if user is None:
        raise refusal(status.HTTP_404_NOT_FOUND, "no_user", f"no user {user_id}")
    return user


def _audit(
    auth: AuthService, principal: Principal, action: str, user_id: str, **detail: object
) -> None:
    auth.audit(
        AuditEntry(
            action,
            "success",
            principal.auth_method,
            user_id=principal.user_id,
            target_kind="user",
            target_id=user_id,
            detail=dict(detail),
        )
    )


def _identity(user_id: str, subject: str) -> UserIdentityRow:
    return UserIdentityRow(
        provider=FORWARD_HEADER_PROVIDER,
        subject=subject.strip(),
        user_id=user_id,
        created_at=utc_now(),
    )


router = APIRouter()


@router.get("/users")
def list_users(request: Request) -> list[UserAdminView]:
    _, store, _ = _context(request)
    return [_view(store, user) for user in store.list_users()]


@router.post("/users", status_code=status.HTTP_201_CREATED)
def create_user(body: UserCreate, request: Request) -> UserAdminView:
    auth, store, principal = _context(request)
    check_username(body.username)
    if body.password is None and not body.subjects:
        raise refusal(
            status.HTTP_422_UNPROCESSABLE_CONTENT,
            "no_way_to_sign_in",
            "give a password, a forwarded subject, or both",
        )
    if body.password is not None:
        check_password(body.password)
    user = UserRow(
        id=new_ulid(),
        username=body.username,
        display_name=body.display_name,
        email=body.email,
        role=body.role.value,
        password_hash=hash_password(body.password) if body.password else None,
        must_change_password=body.password is not None,
        created_at=utc_now(),
    )
    try:
        store.insert_user(user, [_identity(user.id, subject) for subject in body.subjects])
    except (UsernameTakenError, IdentityTakenError) as error:
        raise refusal(status.HTTP_409_CONFLICT, "taken", str(error)) from error
    _audit(auth, principal, "user_create", user.id, role=body.role.value)
    return _view(store, user)


@router.get("/users/{user_id}")
def get_user(user_id: str, request: Request) -> UserAdminView:
    _, store, _ = _context(request)
    return _view(store, _user_or_404(store, user_id))


@router.patch("/users/{user_id}")
def update_user(user_id: str, body: UserUpdate, request: Request) -> UserAdminView:
    """A role change or disabling signs the user out everywhere; never the last admin."""
    auth, store, principal = _context(request)
    user = _user_or_404(store, user_id)
    fields: dict[str, object] = body.model_dump(
        exclude_unset=True, include={"display_name", "email"}
    )
    signs_out = False
    if body.role is not None and body.role.value != user.role:
        fields["role"], signs_out = body.role.value, True
    if body.is_disabled is not None and body.is_disabled != (user.disabled_at is not None):
        fields["disabled_at"] = utc_now() if body.is_disabled else None
        signs_out = signs_out or body.is_disabled
    try:
        updated = store.update_user_keeping_an_admin(user.id, **fields)
    except LastAdminError as error:
        raise refusal(status.HTTP_409_CONFLICT, "last_admin", str(error)) from error
    if signs_out:
        store.delete_user_sessions(user.id)
    _audit(auth, principal, "user_update", user.id, fields=sorted(fields))
    return _view(store, updated or user)


@router.post("/users/{user_id}/password")
def reset_password(user_id: str, request: Request) -> TemporaryPassword:
    """A temporary password, shown once; the user must change it, and is signed out."""
    auth, store, principal = _context(request)
    user = _user_or_404(store, user_id)
    password = issue_temporary_password(store, user.id, utc_now())
    _audit(auth, principal, "password_reset", user.id)
    return TemporaryPassword(password=password)


@router.post("/users/{user_id}/identities", status_code=status.HTTP_201_CREATED)
def link_identity(user_id: str, body: IdentityLink, request: Request) -> UserAdminView:
    auth, store, principal = _context(request)
    user = _user_or_404(store, user_id)
    try:
        store.add_identity(_identity(user.id, body.subject))
    except IdentityTakenError as error:
        raise refusal(status.HTTP_409_CONFLICT, "taken", str(error)) from error
    _audit(auth, principal, "identity_link", user.id, subject=body.subject)
    return _view(store, user)


@router.delete("/users/{user_id}/identities")
def unlink_identity(
    user_id: str,
    subject: Annotated[str, Query(max_length=SUBJECT_MAX_LENGTH)],
    request: Request,
) -> UserAdminView:
    """The subject is a query parameter: DNs hold commas and slashes."""
    auth, store, principal = _context(request)
    user = _user_or_404(store, user_id)
    if not store.remove_identity(user.id, FORWARD_HEADER_PROVIDER, subject):
        raise refusal(status.HTTP_404_NOT_FOUND, "no_identity", "the user has no such subject")
    _audit(auth, principal, "identity_unlink", user.id, subject=subject)
    return _view(store, user)


@router.delete("/users/{user_id}/sessions", status_code=status.HTTP_204_NO_CONTENT)
def revoke_user_sessions(user_id: str, request: Request) -> None:
    auth, store, principal = _context(request)
    user = _user_or_404(store, user_id)
    count = store.delete_user_sessions(user.id)
    _audit(auth, principal, "session_revoke", user.id, count=count)


@router.get("/audit")
def list_audit(
    request: Request,
    limit: Annotated[int, Query(ge=1, le=MAX_AUDIT_PAGE)] = 100,
    before_id: int | None = None,
) -> list[AuditEventView]:
    """Newest first; pass the last id as before_id for the next page."""
    _, store, _ = _context(request)
    return [AuditEventView.of(row) for row in store.list_audit(limit, before_id)]
