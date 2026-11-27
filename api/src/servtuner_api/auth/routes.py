"""The /auth routes: status, login, logout, setup, and a user's own
account. Their access is listed in ``access.ROUTE_ACCESS`` like every other route."""

from datetime import datetime
from typing import Annotated

from fastapi import APIRouter, Header, HTTPException, Request, Response, status
from pydantic import BaseModel, Field

from servtuner_api.auth.principal import ROLE_ACTIONS, Action, AuthMethod, Principal, Role
from servtuner_api.auth.service import AuthService, NewSession
from servtuner_api.database import UserRow, UserSessionRow
from servtuner_api.settings.models import AuthMode, LoginMethod

SETUP_TOKEN_HEADER = "X-Servtuner-Setup-Token"


class UserView(BaseModel):
    id: str | None
    username: str | None
    display_name: str | None = None
    role: Role | None
    auth_method: AuthMethod
    must_change_password: bool = False
    # What the user may do, so the UI hides what the server would refuse (it stays the authority).
    actions: list[Action] = Field(default_factory=list)


class AuthStatus(BaseModel):
    """What the SPA needs at start-up to pick the setup, login or app screen."""

    mode: AuthMode
    methods: list[LoginMethod]
    setup_required: bool
    user: UserView | None
    # The subject the proxy named when it has no servtuner user (forward header).
    refused_subject: str | None = None


class LoginRequest(BaseModel):
    username: str = Field(min_length=1, max_length=128)
    password: str = Field(min_length=1, max_length=256)


class SetupRequest(BaseModel):
    """The first admin. With local accounts: a username and password; with forward-header
    auth only, the forwarded subject becomes the admin and both are omitted."""

    username: str | None = Field(default=None, max_length=128)
    password: str | None = Field(default=None, max_length=256)


class PasswordChange(BaseModel):
    current_password: str = Field(min_length=1, max_length=256)
    new_password: str = Field(min_length=1, max_length=256)


class SessionView(BaseModel):
    id: str  # the token's SHA-256: names the session without being a credential
    created_at: datetime
    last_seen_at: datetime
    expires_at: datetime
    user_agent: str | None
    is_current: bool

    @classmethod
    def of(cls, row: UserSessionRow, current: str | None) -> SessionView:
        return cls(
            id=row.token_hash,
            created_at=row.created_at,
            last_seen_at=row.last_seen_at,
            expires_at=min(row.expires_at, row.idle_expires_at),
            user_agent=row.user_agent,
            is_current=row.token_hash == current,
        )


class RevokedSessions(BaseModel):
    revoked: int


def user_view(principal: Principal, user: UserRow | None = None) -> UserView:
    return UserView(
        id=principal.user_id,
        username=principal.username,
        display_name=user.display_name if user else None,
        role=principal.role,
        auth_method=principal.auth_method,
        must_change_password=principal.must_change_password,
        actions=sorted(principal.actions),
    )


def _auth(request: Request) -> AuthService:
    service: AuthService = request.app.state.auth
    return service


def _principal(request: Request) -> Principal:
    principal: Principal | None = request.state.principal
    if principal is None or principal.user_id is None:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "user auth is off (auth.mode none)")
    return principal


def _set_session_cookie(response: Response, auth: AuthService, session: NewSession) -> None:
    response.set_cookie(
        auth.guard.cookie_name,
        session.token,
        max_age=session.max_age_seconds,
        path="/",
        secure=auth.settings.is_cookie_secure,
        httponly=True,
        samesite="strict",
    )


router = APIRouter()


@router.get("/auth/status")
def auth_status(request: Request, response: Response) -> AuthStatus:
    auth = _auth(request)
    user: UserView | None = None
    refused_subject: str | None = None
    try:
        principal = auth.guard.identify(request)
    except HTTPException as error:
        principal = None
        detail = error.detail if isinstance(error.detail, dict) else {}
        refused_subject = detail.get("subject")
        if auth.guard.cookie_name in request.cookies:
            response.delete_cookie(auth.guard.cookie_name, path="/")
    if principal is not None and principal.user_id is not None:
        stored = auth.store.user(principal.user_id) if auth.store else None
        user = user_view(principal, stored)
    return AuthStatus(
        mode=auth.settings.mode,
        methods=list(auth.settings.methods),
        setup_required=auth.guard.is_setup_required(),
        user=user,
        refused_subject=refused_subject,
    )


@router.post("/auth/login")
def login(body: LoginRequest, request: Request, response: Response) -> UserView:
    auth = _auth(request)
    auth.guard.check_same_origin(request)
    session = auth.login(body.username, body.password, request)
    _set_session_cookie(response, auth, session)
    return _session_user(session)


@router.post("/auth/logout", status_code=status.HTTP_204_NO_CONTENT)
def logout(request: Request, response: Response) -> None:
    auth = _auth(request)
    auth.guard.check_same_origin(request)
    auth.logout(request)
    response.delete_cookie(auth.guard.cookie_name, path="/")


@router.post("/auth/setup", status_code=status.HTTP_201_CREATED)
def setup(
    body: SetupRequest,
    request: Request,
    response: Response,
    setup_token: Annotated[str | None, Header(alias=SETUP_TOKEN_HEADER)] = None,
) -> UserView:
    """Create the first admin with the setup token; 409 once any admin exists."""
    auth = _auth(request)
    auth.guard.check_same_origin(request)
    if body.username is None and body.password is None:
        principal = auth.setup.forwarded_admin(setup_token, request)
        return user_view(principal)
    session = auth.setup.local_admin(setup_token, body.username or "", body.password or "", request)
    _set_session_cookie(response, auth, session)
    return _session_user(session)


@router.get("/auth/me")
def me(request: Request) -> UserView:
    principal = _principal(request)
    auth = _auth(request)
    return user_view(principal, auth.accounts().user(principal.user_id or ""))


@router.post("/auth/password", status_code=status.HTTP_204_NO_CONTENT)
def change_password(body: PasswordChange, request: Request) -> None:
    """Needs the current password; signs out the user's other sessions."""
    _auth(request).change_password(_principal(request), body.current_password, body.new_password)


@router.get("/auth/sessions")
def list_sessions(request: Request) -> list[SessionView]:
    principal = _principal(request)
    rows = _auth(request).sessions_of(principal)
    return [SessionView.of(row, principal.session_id) for row in rows]


@router.delete("/auth/sessions/{session_id}", status_code=status.HTTP_204_NO_CONTENT)
def revoke_session(session_id: str, request: Request) -> None:
    _auth(request).revoke_session(_principal(request), session_id)


@router.post("/auth/sessions/revoke-others")
def revoke_other_sessions(request: Request) -> RevokedSessions:
    """Sign out everywhere else."""
    return RevokedSessions(revoked=_auth(request).revoke_other_sessions(_principal(request)))


def _session_user(session: NewSession) -> UserView:
    user = session.user
    return UserView(
        id=user.id,
        username=user.username,
        display_name=user.display_name,
        role=Role(user.role),
        auth_method=AuthMethod.SESSION,
        must_change_password=user.must_change_password,
        actions=sorted(ROLE_ACTIONS[Role(user.role)]),
    )
