"""The route-to-action table and the guard every route passes through."""

from collections.abc import Iterator

import pytest
from fastapi import HTTPException, Request
from fastapi.routing import APIRoute
from starlette.routing import BaseRoute

from servtuner_api.app import create_app
from servtuner_api.auth.access import ROUTE_ACCESS, Open
from servtuner_api.auth.guard import SESSION_COOKIE_SECURE, AuthGuard
from servtuner_api.auth.principal import (
    ROLE_ACTIONS,
    RUN_TOKEN_ACTIONS,
    UNCHECKED_PRINCIPAL,
    Action,
    AuthMethod,
    Principal,
    Role,
    user_principal,
)
from servtuner_api.settings.models import AuthSettings, LoginMethod

TEST_USER_HEADER = "x-test-role"
RUN_ID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
USER_ACTIONS = [action for action in Action if action not in RUN_TOKEN_ACTIONS]


def api_routes(routes: list[BaseRoute]) -> Iterator[APIRoute]:
    """Every APIRoute, looking inside the routers FastAPI keeps whole when it includes them."""
    for route in routes:
        if isinstance(route, APIRoute):
            yield route
        included = getattr(route, "original_router", None)
        if included is not None:
            yield from api_routes(included.routes)


def app_routes() -> set[tuple[str, str]]:
    app = create_app(lambda: None)  # type: ignore[arg-type, return-value]  # routes only
    return {
        (method, route.path) for route in api_routes(app.routes) for method in route.methods or ()
    }


def test_every_route_declares_its_access() -> None:
    missing = app_routes() - ROUTE_ACCESS.keys()
    assert not missing, f"routes with no entry in auth/access.py: {sorted(missing)}"


def test_the_table_lists_no_route_that_is_gone() -> None:
    stale = ROUTE_ACCESS.keys() - app_routes()
    assert not stale, f"entries in auth/access.py for routes that no longer exist: {sorted(stale)}"


def test_only_the_job_route_takes_a_run_token() -> None:
    job_routes = [route for route, access in ROUTE_ACCESS.items() if access is Action.EVENTS_WRITE]
    assert job_routes == [("POST", "/runs/{run_id}/events")]


def test_public_routes_stay_few() -> None:
    public = {route for route, access in ROUTE_ACCESS.items() if access is Open.PUBLIC}
    assert public <= {
        ("GET", "/healthz"),
        ("GET", "/auth/status"),
        ("POST", "/auth/login"),
        ("POST", "/auth/logout"),
        ("POST", "/auth/setup"),
    }


class HeaderAuthenticator:
    """Signs a request in as the role its test header names; 'disabled' raises like a real
    authenticator facing a credential that is present but invalid."""

    method = AuthMethod.SESSION

    def __init__(self, must_change_password: bool = False) -> None:
        self.must_change_password = must_change_password

    def authenticate(self, request: Request) -> Principal | None:
        role = request.headers.get(TEST_USER_HEADER)
        if role is None:
            return None
        return user_principal(
            "01USER0000000000000000000A",
            "someone",
            Role(role),
            AuthMethod.SESSION,
            session_id="hash",
            must_change_password=self.must_change_password,
        )


def guard(*, is_setup_required: bool = False, must_change_password: bool = False) -> AuthGuard:
    return AuthGuard(
        AuthSettings(methods=(LoginMethod.LOCAL,)),
        [HeaderAuthenticator(must_change_password)],
        lambda: is_setup_required,
    )


def request(
    method: str = "GET", headers: dict[str, str] | None = None, run_id: str | None = None
) -> Request:
    scope = {
        "type": "http",
        "method": method,
        "path": "/",
        "query_string": b"",
        "headers": [
            (name.lower().encode(), value.encode()) for name, value in (headers or {}).items()
        ],
        "path_params": {"run_id": run_id} if run_id else {},
    }
    return Request(scope)


def refusal_of(guard_: AuthGuard, req: Request, access: Action | Open) -> tuple[int, str]:
    with pytest.raises(HTTPException) as caught:
        guard_.authorize(req, access)
    detail = caught.value.detail
    assert isinstance(detail, dict)
    return caught.value.status_code, detail["code"]


@pytest.mark.parametrize("role", list(Role))
@pytest.mark.parametrize("action", USER_ACTIONS)
def test_roles_grant_exactly_their_actions(role: Role, action: Action) -> None:
    req = request("GET", {TEST_USER_HEADER: role.value})
    if action in ROLE_ACTIONS[role]:
        principal = guard().authorize(req, action)
        assert principal is not None and principal.role is role
    else:
        assert refusal_of(guard(), req, action) == (403, "forbidden")


def test_roles_are_nested() -> None:
    assert ROLE_ACTIONS[Role.VIEWER] < ROLE_ACTIONS[Role.OPERATOR] < ROLE_ACTIONS[Role.ADMIN]
    assert Action.EVENTS_WRITE not in ROLE_ACTIONS[Role.ADMIN]


def test_no_credential_is_401() -> None:
    assert refusal_of(guard(), request(), Action.READ) == (401, "unauthenticated")


def test_before_setup_every_route_but_the_public_ones_says_so() -> None:
    req = request("GET", {TEST_USER_HEADER: "admin"})
    assert refusal_of(guard(is_setup_required=True), req, Action.READ) == (401, "setup_required")
    assert guard(is_setup_required=True).authorize(request(), Open.PUBLIC) is None


def test_a_bearer_token_on_a_user_route_is_refused() -> None:
    req = request("GET", {"Authorization": "Bearer abc", TEST_USER_HEADER: "admin"})
    assert refusal_of(guard(), req, Action.READ) == (401, "bearer_not_accepted")


def test_the_job_route_takes_only_a_run_token() -> None:
    job = Action.EVENTS_WRITE
    bearer = {"Authorization": "Bearer abc"}
    principal = guard().authorize(request("POST", bearer, RUN_ID), job)
    assert principal is not None
    assert (principal.auth_method, principal.run_id) == (AuthMethod.RUN_TOKEN, RUN_ID)
    assert principal.actions == RUN_TOKEN_ACTIONS
    assert refusal_of(guard(), request("POST", {}, RUN_ID), job) == (401, "missing_run_token")
    forwarded = {**bearer, "X-Forwarded-User": "admin"}
    assert refusal_of(guard(), request("POST", forwarded, RUN_ID), job) == (
        400,
        "forward_header_on_job_request",
    )
    with_cookie = {**bearer, "Cookie": f"{SESSION_COOKIE_SECURE}=abc"}
    assert refusal_of(guard(), request("POST", with_cookie, RUN_ID), job) == (403, "run_token_only")


def test_the_datasheet_route_is_a_job_route_too() -> None:
    principal = guard().authorize(
        request("GET", {"Authorization": "Bearer abc"}, RUN_ID), Action.DATASHEET_READ
    )
    assert principal is not None and principal.run_id == RUN_ID
    req = request("GET", {TEST_USER_HEADER: "admin"}, RUN_ID)
    assert refusal_of(guard(), req, Action.DATASHEET_READ) == (401, "missing_run_token")


def test_a_user_cannot_use_the_job_route() -> None:
    req = request("POST", {TEST_USER_HEADER: "admin"}, RUN_ID)
    assert refusal_of(guard(), req, Action.EVENTS_WRITE) == (401, "missing_run_token")


@pytest.mark.parametrize(
    "headers",
    [
        {"Sec-Fetch-Site": "cross-site"},
        {"Sec-Fetch-Site": "same-site"},
        {"Origin": "https://elsewhere.example", "Host": "servtuner.example"},
    ],
)
def test_cross_site_writes_are_refused(headers: dict[str, str]) -> None:
    req = request("POST", {TEST_USER_HEADER: "admin", **headers})
    assert refusal_of(guard(), req, Action.RUNS_WRITE) == (403, "cross_site")


@pytest.mark.parametrize(
    "headers",
    [
        {"Sec-Fetch-Site": "same-origin"},
        {"Origin": "https://servtuner.example", "Host": "servtuner.example"},
        {
            "Origin": "https://servtuner.example",
            "X-Forwarded-Host": "servtuner.example",
            "Host": "api",
        },
        {},  # not a browser: no other site can make it send the credential
    ],
)
def test_same_site_writes_pass(headers: dict[str, str]) -> None:
    req = request("POST", {TEST_USER_HEADER: "admin", **headers})
    assert guard().authorize(req, Action.RUNS_WRITE) is not None


def test_reads_need_no_origin() -> None:
    req = request("GET", {TEST_USER_HEADER: "viewer", "Sec-Fetch-Site": "cross-site"})
    assert guard().authorize(req, Action.READ) is not None


def test_a_reset_password_blocks_everything_but_the_users_own_routes() -> None:
    req = request("GET", {TEST_USER_HEADER: "admin"})
    blocked = guard(must_change_password=True)
    assert refusal_of(blocked, req, Action.READ) == (403, "password_change_required")
    assert blocked.authorize(req, Open.AUTHENTICATED) is not None


def test_mode_none_checks_nothing() -> None:
    unchecked = AuthGuard.unchecked()
    assert unchecked.authorize(request(), Action.USERS_ADMIN) is UNCHECKED_PRINCIPAL
    assert UNCHECKED_PRINCIPAL.is_bypassed and UNCHECKED_PRINCIPAL.user_id is None
    assert not unchecked.is_setup_required()
