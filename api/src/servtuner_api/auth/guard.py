"""The one dependency every route passes through.

It looks the route up in ``access.ROUTE_ACCESS``, resolves the caller with the configured
authenticators, and refuses what the caller may not do. Handlers read the result from
``request.state.principal``.
"""

import logging
from collections.abc import Callable, Sequence
from typing import Protocol
from urllib.parse import urlsplit

from fastapi import HTTPException, Request, status
from starlette.concurrency import run_in_threadpool

from servtuner_api.auth.access import Open, RouteAccess, access_of
from servtuner_api.auth.attribution import acting_user_id
from servtuner_api.auth.principal import (
    RUN_TOKEN_ACTIONS,
    UNCHECKED_PRINCIPAL,
    AuthMethod,
    Principal,
    run_token_principal,
)
from servtuner_api.settings.models import AuthMode, AuthSettings

logger = logging.getLogger(__name__)

SESSION_COOKIE_SECURE = "__Host-servtuner_session"
# Without Secure a browser refuses the __Host- prefix, so a plain-HTTP install uses this name.
SESSION_COOKIE_PLAIN = "servtuner_session"
SAFE_METHODS = frozenset({"GET", "HEAD", "OPTIONS"})
BEARER_PREFIX = "Bearer "
# Ambient credentials: a browser sends them on its own, so they need the CSRF check.
AMBIENT_METHODS = frozenset({AuthMethod.SESSION, AuthMethod.FORWARD_HEADER})


def session_cookie_name(settings: AuthSettings) -> str:
    return SESSION_COOKIE_SECURE if settings.is_cookie_secure else SESSION_COOKIE_PLAIN


def refusal(status_code: int, code: str, message: str, **extra: str) -> HTTPException:
    """Every auth refusal has one shape: {"detail": {"code", "message", ...}}."""
    headers = (
        {"WWW-Authenticate": "Cookie"} if status_code == status.HTTP_401_UNAUTHORIZED else None
    )
    detail = {"code": code, "message": message, **extra}
    return HTTPException(status_code, detail=detail, headers=headers)


class Authenticator(Protocol):
    """Turns a request into a principal, or passes with None. A credential that is present but
    invalid raises a refusal; it never falls through to the next authenticator."""

    method: AuthMethod

    def authenticate(self, request: Request) -> Principal | None: ...


class AuthGuard:
    def __init__(
        self,
        settings: AuthSettings,
        authenticators: Sequence[Authenticator],
        is_setup_required: Callable[[], bool],
    ) -> None:
        self.settings = settings
        self._authenticators = tuple(authenticators)
        self._is_setup_required = is_setup_required

    @classmethod
    def unchecked(cls) -> AuthGuard:
        """auth.mode none: everything is allowed, as before user auth."""
        return cls(AuthSettings(mode=AuthMode.NONE, methods=()), (), lambda: False)

    @property
    def cookie_name(self) -> str:
        return session_cookie_name(self.settings)

    @property
    def is_checking(self) -> bool:
        return self.settings.mode is AuthMode.LOCAL

    def is_setup_required(self) -> bool:
        return self.is_checking and self._is_setup_required()

    def identify(self, request: Request) -> Principal | None:
        """The signed-in user, or None; for public routes that show who is signed in."""
        if not self.is_checking:
            return UNCHECKED_PRINCIPAL
        for authenticator in self._authenticators:
            principal = authenticator.authenticate(request)
            if principal is not None:
                return principal
        return None

    def authorize(self, request: Request, access: RouteAccess) -> Principal | None:
        if access is Open.PUBLIC:
            return None
        if access in RUN_TOKEN_ACTIONS:
            return self._run_token_channel(request)
        if not self.is_checking:
            return UNCHECKED_PRINCIPAL
        if _bearer(request) is not None:
            raise refusal(
                status.HTTP_401_UNAUTHORIZED,
                "bearer_not_accepted",
                "bearer tokens are accepted only on a run's own job routes"
                " (POST /runs/{run_id}/events, GET /runs/{run_id}/datasheet)",
            )
        if self.is_setup_required():
            raise refusal(
                status.HTTP_401_UNAUTHORIZED,
                "setup_required",
                "servtuner has no admin yet: create one with the setup token (POST /auth/setup)",
            )
        principal = self.identify(request)
        if principal is None:
            raise refusal(status.HTTP_401_UNAUTHORIZED, "unauthenticated", "sign in first")
        if principal.auth_method in AMBIENT_METHODS and request.method not in SAFE_METHODS:
            self.check_same_origin(request)
        if principal.must_change_password and access is not Open.AUTHENTICATED:
            raise refusal(
                status.HTTP_403_FORBIDDEN,
                "password_change_required",
                "change your password before doing anything else",
            )
        if access is Open.AUTHENTICATED or principal.can(access):
            return principal
        raise refusal(
            status.HTTP_403_FORBIDDEN,
            "forbidden",
            f"this needs the {access.value} permission, which the {principal.role} role lacks",
        )

    def _run_token_channel(self, request: Request) -> Principal:
        """A job's route takes a run token and nothing else. The token itself is checked
        against the run by the handler; here the channel is kept apart from users."""
        if request.headers.get(self.settings.forward_header.user_header) is not None:
            raise refusal(
                status.HTTP_400_BAD_REQUEST,
                "forward_header_on_job_request",
                "a request with a run token may not name a user",
            )
        if self.cookie_name in request.cookies:
            raise refusal(
                status.HTTP_403_FORBIDDEN,
                "run_token_only",
                "only a run token is accepted here",
            )
        if _bearer(request) is None:
            raise refusal(
                status.HTTP_401_UNAUTHORIZED, "missing_run_token", "missing bearer run token"
            )
        return run_token_principal(request.path_params.get("run_id", ""))

    def check_same_origin(self, request: Request) -> None:
        """CSRF: an unsafe request with an ambient credential must come from this site.
        A request with neither header is not from a browser, so no other site can forge it."""
        fetch_site = request.headers.get("sec-fetch-site")
        if fetch_site is not None:
            if fetch_site == "same-origin":
                return
        else:
            origin = request.headers.get("origin")
            if origin is None:
                return
            host = request.headers.get("x-forwarded-host") or request.headers.get("host")
            if urlsplit(origin).netloc == host:
                return
        raise refusal(
            status.HTTP_403_FORBIDDEN, "cross_site", "the request did not come from this site"
        )


def _bearer(request: Request) -> str | None:
    header = request.headers.get("authorization", "")
    return header.removeprefix(BEARER_PREFIX) if header.startswith(BEARER_PREFIX) else None


async def authorize_request(request: Request) -> None:
    """The app-wide dependency: refuse unlisted routes, then apply the route's access.

    It is async so that the acting user it sets is in the request's own context, which
    the endpoint and its thread inherit; the checks themselves touch the database, so they run
    in a worker thread.
    """
    route = request.scope.get("route")
    path = getattr(route, "path", None)
    access = access_of(request.method, path) if path else None
    if access is None:
        logger.error("route %s %s has no access rule; refused", request.method, path)
        raise refusal(status.HTTP_403_FORBIDDEN, "no_access_rule", "this route is not available")
    guard: AuthGuard = request.app.state.auth.guard
    principal = await run_in_threadpool(guard.authorize, request, access)
    request.state.principal = principal
    acting_user_id.set(principal.user_id if principal else None)
