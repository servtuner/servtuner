"""Local accounts, setup and sessions."""

import hashlib
from collections.abc import Iterator
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session

from servtuner_api.auth import service as auth_service
from servtuner_api.auth.principal import Role
from servtuner_api.database import AuditEventRow, UserSessionRow
from servtuner_api.settings.models import AuthSettings
from tests.support.auth import PASSWORD, SETUP_TOKEN, AuthApp, auth_app, auth_settings
from tests.support.specs import event_body, run_spec_bytes

COOKIE = "__Host-servtuner_session"
SETUP = {"X-Servtuner-Setup-Token": SETUP_TOKEN}
ADMIN = {"username": "first-admin", "password": PASSWORD}


@pytest.fixture
def app(engine: Engine, data_dir: Path, tmp_path: Path) -> Iterator[AuthApp]:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as running:
        yield running


@pytest.fixture
def admin_app(app: AuthApp) -> AuthApp:
    assert app.client.post("/auth/setup", json=ADMIN, headers=SETUP).status_code == 201
    return app


def code_of(response: object) -> str:
    detail = response.json()["detail"]  # type: ignore[attr-defined]
    return str(detail["code"])


def audit_actions(engine: Engine) -> list[tuple[str, str]]:
    with Session(engine) as session:
        rows = session.scalars(select(AuditEventRow).order_by(AuditEventRow.id))
        return [(row.action, row.outcome) for row in rows]


# Setup


def test_before_setup_everything_but_public_routes_asks_for_it(app: AuthApp) -> None:
    response = app.client.get("/runs")
    assert (response.status_code, code_of(response)) == (401, "setup_required")
    status = app.client.get("/auth/status").json()
    assert status["setup_required"] is True and status["user"] is None
    assert status["mode"] == "local" and status["methods"] == ["local"]
    assert app.client.get("/healthz").status_code == 200


def test_setup_fails_closed_without_a_token_file(
    engine: Engine, data_dir: Path, tmp_path: Path
) -> None:
    with auth_app(engine, data_dir, AuthSettings()) as running:
        response = running.client.post("/auth/setup", json=ADMIN, headers=SETUP)
        assert (response.status_code, code_of(response)) == (403, "setup_token_not_configured")


def test_setup_needs_the_right_token(app: AuthApp, engine: Engine) -> None:
    missing = app.client.post("/auth/setup", json=ADMIN)
    assert (missing.status_code, code_of(missing)) == (401, "missing_setup_token")
    wrong = app.client.post("/auth/setup", json=ADMIN, headers={"X-Servtuner-Setup-Token": "nope"})
    assert (wrong.status_code, code_of(wrong)) == (403, "invalid_setup_token")
    assert ("setup", "refused") in audit_actions(engine)


def test_setup_checks_the_password(app: AuthApp) -> None:
    weak = app.client.post("/auth/setup", json={**ADMIN, "password": "short"}, headers=SETUP)
    assert (weak.status_code, code_of(weak)) == (422, "weak_password")
    bad_name = app.client.post("/auth/setup", json={**ADMIN, "username": "a b"}, headers=SETUP)
    assert (bad_name.status_code, code_of(bad_name)) == (422, "invalid_username")


def test_setup_makes_the_admin_signs_them_in_and_closes(admin_app: AuthApp) -> None:
    client = admin_app.client
    assert client.get("/runs").status_code == 200
    me = client.get("/auth/me").json()
    assert (me["username"], me["role"], me["auth_method"]) == ("first-admin", "admin", "session")
    assert "users.admin" in me["actions"] and "events.write" not in me["actions"]
    again = client.post("/auth/setup", json={**ADMIN, "username": "second"}, headers=SETUP)
    assert (again.status_code, code_of(again)) == (409, "setup_done")
    assert client.get("/auth/status").json()["setup_required"] is False


# Sessions and cookies


def test_the_session_cookie_is_locked_down(app: AuthApp) -> None:
    response = app.client.post("/auth/setup", json=ADMIN, headers=SETUP)
    cookie = response.headers["set-cookie"]
    assert cookie.startswith(f"{COOKIE}=")
    for attribute in ("HttpOnly", "Secure", "Path=/", "SameSite=strict"):
        assert attribute in cookie, cookie


def test_only_the_tokens_hash_is_stored(admin_app: AuthApp, engine: Engine) -> None:
    token = admin_app.client.cookies[COOKIE]
    with Session(engine) as session:
        (stored,) = session.scalars(select(UserSessionRow.token_hash))
    assert stored == hashlib.sha256(token.encode()).hexdigest() and stored != token


def test_logout_ends_the_session(admin_app: AuthApp) -> None:
    client = admin_app.client
    token = client.cookies[COOKIE]
    assert client.post("/auth/logout").status_code == 204
    client.cookies.set(COOKIE, token)
    response = client.get("/runs")
    assert (response.status_code, code_of(response)) == (401, "session_ended")


def test_an_idle_session_ends(admin_app: AuthApp) -> None:
    admin_app.clock.advance(AuthSettings().session_idle_seconds + 1)
    response = admin_app.client.get("/runs")
    assert (response.status_code, code_of(response)) == (401, "session_ended")


def test_use_keeps_a_session_alive_until_its_absolute_end(admin_app: AuthApp) -> None:
    settings = AuthSettings()
    step = settings.session_idle_seconds - 60
    elapsed = 0
    while elapsed + step < settings.session_absolute_seconds:
        admin_app.clock.advance(step)
        elapsed += step
        assert admin_app.client.get("/runs").status_code == 200
    admin_app.clock.advance(settings.session_absolute_seconds - elapsed + 1)
    assert admin_app.client.get("/runs").status_code == 401


def test_a_stale_cookie_does_not_break_status(admin_app: AuthApp) -> None:
    admin_app.client.cookies.set(COOKIE, "made-up")
    status = admin_app.client.get("/auth/status")
    assert status.status_code == 200 and status.json()["user"] is None


# Login and lockout


def test_wrong_password_and_unknown_user_look_the_same(admin_app: AuthApp) -> None:
    admin_app.client.cookies.clear()
    wrong = admin_app.client.post("/auth/login", json={**ADMIN, "password": "wrong password!"})
    unknown = admin_app.client.post("/auth/login", json={"username": "nobody", "password": "x"})
    assert wrong.status_code == unknown.status_code == 401
    assert wrong.json() == unknown.json()


def test_every_failure_costs_one_hash(admin_app: AuthApp, monkeypatch: pytest.MonkeyPatch) -> None:
    calls: list[str] = []
    real = auth_service.verify_password

    def counting(password: str, stored: str) -> bool:
        calls.append(stored)
        return real(password, stored)

    monkeypatch.setattr(auth_service, "verify_password", counting)
    admin_app.client.post("/auth/login", json={"username": "nobody", "password": "whatever!"})
    admin_app.client.post("/auth/login", json={**ADMIN, "password": "wrong password!"})
    assert len(calls) == 2
    assert calls[0] == auth_service.DUMMY_HASH


def test_five_failures_lock_the_username(admin_app: AuthApp, engine: Engine) -> None:
    client = admin_app.client
    for _ in range(AuthSettings().lockout_max_failures):
        response = client.post("/auth/login", json={**ADMIN, "password": "wrong password!"})
        assert response.status_code == 401
    locked = client.post("/auth/login", json=ADMIN)
    assert (locked.status_code, code_of(locked)) == (429, "locked")
    assert int(locked.headers["retry-after"]) > 0
    assert ("lockout", "refused") in audit_actions(engine)
    admin_app.clock.advance(AuthSettings().lockout_window_seconds + 1)
    assert client.post("/auth/login", json=ADMIN).status_code == 200


def test_the_lock_is_shared_by_every_api_instance(
    engine: Engine, data_dir: Path, tmp_path: Path
) -> None:
    settings = auth_settings(tmp_path)
    with (
        auth_app(engine, data_dir, settings) as first,
        auth_app(engine, data_dir, settings) as second,
    ):
        first.client.post("/auth/setup", json=ADMIN, headers=SETUP)
        second.clock.now = first.clock.now
        for app in (first, second, first, second, first):
            app.client.post("/auth/login", json={**ADMIN, "password": "wrong password!"})
        assert second.client.post("/auth/login", json=ADMIN).status_code == 429


def test_a_disabled_user_is_signed_out(admin_app: AuthApp) -> None:
    viewer = admin_app.add_user("watcher", Role.VIEWER)
    admin_app.login("watcher")
    admin_app.store.update_user(viewer.id, disabled_at=admin_app.clock())
    response = admin_app.client.get("/runs")
    assert (response.status_code, code_of(response)) == (401, "user_disabled")
    login = admin_app.client.post("/auth/login", json={"username": "watcher", "password": PASSWORD})
    assert login.status_code == 401


# Roles


@pytest.mark.parametrize(
    ("role", "can_write"), [(Role.VIEWER, False), (Role.OPERATOR, True), (Role.ADMIN, True)]
)
def test_roles_through_a_real_session(admin_app: AuthApp, role: Role, can_write: bool) -> None:
    admin_app.add_user("someone", role)
    client = admin_app.login("someone")
    assert client.get("/runs").status_code == 200
    created = client.post("/runs", content=run_spec_bytes())
    assert created.status_code == (201 if can_write else 403)
    users = client.delete("/runs/01ARZ3NDEKTSV4RRFFQ69G5FAV")
    assert users.status_code == (404 if role is Role.ADMIN else 403)


def test_a_session_cannot_post_events(admin_app: AuthApp) -> None:
    run_id = admin_app.client.post("/runs", content=run_spec_bytes()).json()["id"]
    response = admin_app.client.post(f"/runs/{run_id}/events", content=event_body(run_id, 0))
    assert (response.status_code, code_of(response)) == (403, "run_token_only")
    token = admin_app.runner.requests[run_id].run_token
    with_cookie = admin_app.client.post(
        f"/runs/{run_id}/events",
        content=event_body(run_id, 0),
        headers={"Authorization": f"Bearer {token}"},
    )
    assert (with_cookie.status_code, code_of(with_cookie)) == (403, "run_token_only")


def test_cross_site_writes_are_refused(admin_app: AuthApp) -> None:
    response = admin_app.client.post(
        "/runs", content=run_spec_bytes(), headers={"Sec-Fetch-Site": "cross-site"}
    )
    assert (response.status_code, code_of(response)) == (403, "cross_site")


# A user's own account


def test_changing_the_password_needs_the_current_one(admin_app: AuthApp) -> None:
    body = {"current_password": "not it at all", "new_password": "a brand new passphrase"}
    response = admin_app.client.post("/auth/password", json=body)
    assert (response.status_code, code_of(response)) == (403, "wrong_password")


def test_changing_the_password_signs_out_the_other_sessions(
    engine: Engine, data_dir: Path, tmp_path: Path
) -> None:
    settings = auth_settings(tmp_path)
    with (
        auth_app(engine, data_dir, settings) as first,
        auth_app(engine, data_dir, settings) as other,
    ):
        first.client.post("/auth/setup", json=ADMIN, headers=SETUP)
        other.clock.now = first.clock.now
        other.login("first-admin")
        body = {"current_password": PASSWORD, "new_password": "a brand new passphrase"}
        assert first.client.post("/auth/password", json=body).status_code == 204
        assert first.client.get("/runs").status_code == 200
        assert other.client.get("/runs").status_code == 401
        assert (
            other.client.post(
                "/auth/login", json={**ADMIN, "password": "a brand new passphrase"}
            ).status_code
            == 200
        )


def test_a_reset_password_must_be_changed_first(admin_app: AuthApp) -> None:
    admin_app.add_user("reset-me", Role.OPERATOR, must_change_password=True)
    client = admin_app.login("reset-me")
    blocked = client.get("/runs")
    assert (blocked.status_code, code_of(blocked)) == (403, "password_change_required")
    assert client.get("/auth/me").json()["must_change_password"] is True
    body = {"current_password": PASSWORD, "new_password": "a brand new passphrase"}
    assert client.post("/auth/password", json=body).status_code == 204
    assert client.get("/runs").status_code == 200


def test_sessions_can_be_listed_and_revoked(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    settings = auth_settings(tmp_path)
    with (
        auth_app(engine, data_dir, settings) as first,
        auth_app(engine, data_dir, settings) as other,
    ):
        first.client.post("/auth/setup", json=ADMIN, headers=SETUP)
        other.clock.now = first.clock.now
        other.login("first-admin")
        sessions = first.client.get("/auth/sessions").json()
        assert len(sessions) == 2
        assert sum(session["is_current"] for session in sessions) == 1
        revoked = first.client.post("/auth/sessions/revoke-others").json()
        assert revoked == {"revoked": 1}
        assert other.client.get("/runs").status_code == 401
        (mine,) = first.client.get("/auth/sessions").json()
        assert first.client.delete(f"/auth/sessions/{mine['id']}").status_code == 204
        assert first.client.get("/runs").status_code == 401


# auth.mode none


def test_mode_none_has_no_user_routes(client: object) -> None:
    status = client.get("/auth/status").json()  # type: ignore[attr-defined]
    assert status["mode"] == "none" and status["setup_required"] is False
    login = client.post("/auth/login", json=ADMIN)  # type: ignore[attr-defined]
    assert login.status_code == 404
    assert client.get("/auth/me").status_code == 404  # type: ignore[attr-defined]
