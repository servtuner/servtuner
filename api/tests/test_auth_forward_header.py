"""Forward-header auth: the user a trusted proxy names."""

from collections.abc import Iterator
from pathlib import Path

import pytest
from sqlalchemy import select
from sqlalchemy.engine import Engine
from sqlalchemy.orm import Session

from servtuner_api.auth.forward_header import FORWARD_HEADER_PROVIDER
from servtuner_api.auth.principal import Role
from servtuner_api.database import AuditEventRow, UserIdentityRow, UserRow
from servtuner_api.settings.models import ForwardHeaderSettings
from tests.support.auth import (
    BOTH_METHODS,
    FORWARD_ONLY,
    PASSWORD,
    SETUP_TOKEN,
    VIEWER_PROVISIONING,
    AuthApp,
    auth_app,
    auth_settings,
)
from tests.support.specs import run_spec_bytes

USER_HEADER = "X-Forwarded-User"
ADMIN_SUBJECT = "CN=First Admin,OU=Platform,O=Example"
SETUP = {"X-Servtuner-Setup-Token": SETUP_TOKEN}


def as_subject(subject: str) -> dict[str, str]:
    return {USER_HEADER: subject}


def code_of(response: object) -> str:
    return str(response.json()["detail"]["code"])  # type: ignore[attr-defined]


@pytest.fixture
def app(engine: Engine, data_dir: Path, tmp_path: Path) -> Iterator[AuthApp]:
    with auth_app(engine, data_dir, auth_settings(tmp_path, methods=FORWARD_ONLY)) as running:
        yield running


@pytest.fixture
def admin_app(app: AuthApp) -> AuthApp:
    response = app.client.post(
        "/auth/setup", json={}, headers={**SETUP, **as_subject(ADMIN_SUBJECT)}
    )
    assert response.status_code == 201, response.text
    return app


def link(app: AuthApp, username: str, role: Role, subject: str) -> UserRow:
    user = app.add_user(username, role, password=None)
    app.store.add_identity(
        UserIdentityRow(
            provider=FORWARD_HEADER_PROVIDER,
            subject=subject,
            user_id=user.id,
            created_at=app.clock(),
        )
    )
    return user


def test_the_forwarded_subject_becomes_the_first_admin(admin_app: AuthApp) -> None:
    me = admin_app.client.get("/auth/me", headers=as_subject(ADMIN_SUBJECT)).json()
    assert (me["role"], me["auth_method"]) == ("admin", "forward_header")
    # A DN is not a valid username, so one is made from it; the subject itself is the key.
    assert me["username"] == "CN-First-Admin-OU-Platform-O-Example"
    assert admin_app.client.get("/runs", headers=as_subject(ADMIN_SUBJECT)).status_code == 200


def test_setup_through_the_proxy_needs_the_token_and_the_header(app: AuthApp) -> None:
    no_token = app.client.post("/auth/setup", json={}, headers=as_subject(ADMIN_SUBJECT))
    assert no_token.status_code == 401
    no_header = app.client.post("/auth/setup", json={}, headers=SETUP)
    assert (no_header.status_code, code_of(no_header)) == (400, "missing_forwarded_subject")


def test_before_setup_status_shows_who_the_proxy_named(app: AuthApp) -> None:
    status = app.client.get("/auth/status", headers=as_subject(ADMIN_SUBJECT)).json()
    assert status["setup_required"] is True
    assert status["refused_subject"] == ADMIN_SUBJECT


def test_no_local_login_when_only_the_proxy_is_trusted(admin_app: AuthApp) -> None:
    response = admin_app.client.post("/auth/login", json={"username": "x", "password": PASSWORD})
    assert (response.status_code, code_of(response)) == (404, "local_login_off")


def test_an_unknown_subject_is_refused_and_audited(admin_app: AuthApp, engine: Engine) -> None:
    response = admin_app.client.get("/runs", headers=as_subject("stranger@example.com"))
    assert (response.status_code, code_of(response)) == (403, "unknown_identity")
    assert response.json()["detail"]["subject"] == "stranger@example.com"
    with Session(engine) as session:
        refused = session.scalars(
            select(AuditEventRow).where(AuditEventRow.outcome == "refused")
        ).all()
    assert [(row.action, row.subject) for row in refused] == [
        ("forward_header", "stranger@example.com")
    ]
    status = admin_app.client.get("/auth/status", headers=as_subject("stranger@example.com"))
    assert status.json()["refused_subject"] == "stranger@example.com"


def test_a_linked_subject_signs_in_with_its_users_role(admin_app: AuthApp) -> None:
    link(admin_app, "viewer-one", Role.VIEWER, "viewer@example.com")
    headers = as_subject("viewer@example.com")
    assert admin_app.client.get("/runs", headers=headers).status_code == 200
    assert (
        admin_app.client.post("/runs", content=run_spec_bytes(), headers=headers).status_code == 403
    )


def test_subjects_match_exactly(admin_app: AuthApp) -> None:
    link(admin_app, "dn-user", Role.VIEWER, "CN=Jo Doe,O=Example")
    assert (
        admin_app.client.get("/runs", headers=as_subject("CN=Jo Doe,O=Example")).status_code == 200
    )
    assert (
        admin_app.client.get("/runs", headers=as_subject("cn=jo doe,o=example")).status_code == 403
    )


def test_viewer_provisioning_makes_a_viewer(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    settings = auth_settings(tmp_path, methods=FORWARD_ONLY, forward_header=VIEWER_PROVISIONING)
    with auth_app(engine, data_dir, settings) as app:
        app.client.post("/auth/setup", json={}, headers={**SETUP, **as_subject(ADMIN_SUBJECT)})
        headers = as_subject("new.person@example.com")
        assert app.client.get("/runs", headers=headers).status_code == 200
        me = app.client.get("/auth/me", headers=headers).json()
        assert (me["role"], me["username"]) == ("viewer", "new.person@example.com")
        assert app.client.get("/runs", headers=headers).status_code == 200  # same user again
        assert len(app.store.list_users()) == 2


def test_profile_headers_fill_a_new_user(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    forward = ForwardHeaderSettings(
        display_name_header="X-Forwarded-Name", email_header="X-Forwarded-Email"
    )
    settings = auth_settings(tmp_path, methods=FORWARD_ONLY, forward_header=forward)
    with auth_app(engine, data_dir, settings) as app:
        headers = {
            **SETUP,
            **as_subject("ada"),
            "X-Forwarded-Name": "Ada L.",
            "X-Forwarded-Email": "ada@example.com",
        }
        app.client.post("/auth/setup", json={}, headers=headers)
        (user,) = app.store.list_users()
        assert (user.display_name, user.email) == ("Ada L.", "ada@example.com")


@pytest.mark.parametrize(
    "headers",
    [
        [(USER_HEADER, "client-sent"), (USER_HEADER, "proxy-sent")],  # the proxy appended
        [(USER_HEADER, "x" * 513)],
        [(USER_HEADER, "bad\x01subject")],
    ],
)
def test_malformed_subjects_are_refused(admin_app: AuthApp, headers: list[tuple[str, str]]) -> None:
    response = admin_app.client.get("/runs", headers=headers)
    assert (response.status_code, code_of(response)) == (400, "invalid_forwarded_subject")


def test_a_disabled_user_is_refused_at_once(admin_app: AuthApp) -> None:
    user = link(admin_app, "leaver", Role.OPERATOR, "leaver@example.com")
    assert (
        admin_app.client.get("/runs", headers=as_subject("leaver@example.com")).status_code == 200
    )
    admin_app.store.update_user(user.id, disabled_at=admin_app.clock())
    response = admin_app.client.get("/runs", headers=as_subject("leaver@example.com"))
    assert (response.status_code, code_of(response)) == (401, "user_disabled")


def test_cross_site_writes_are_refused(admin_app: AuthApp) -> None:
    headers = {**as_subject(ADMIN_SUBJECT), "Sec-Fetch-Site": "cross-site"}
    response = admin_app.client.post("/runs", content=run_spec_bytes(), headers=headers)
    assert (response.status_code, code_of(response)) == (403, "cross_site")


def test_with_both_methods_the_header_comes_first_and_local_stays(
    engine: Engine, data_dir: Path, tmp_path: Path
) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path, methods=BOTH_METHODS)) as app:
        body = {"username": "break-glass", "password": PASSWORD}
        assert app.client.post("/auth/setup", json=body, headers=SETUP).status_code == 201
        link(app, "viewer-two", Role.VIEWER, "v2@example.com")
        # The session says admin, the header says viewer: the header wins, being first.
        me = app.client.get("/auth/me", headers=as_subject("v2@example.com")).json()
        assert (me["username"], me["role"]) == ("viewer-two", "viewer")
        assert app.client.get("/auth/me").json()["username"] == "break-glass"


def test_a_forward_header_never_rides_on_a_run_token(admin_app: AuthApp) -> None:
    run_id = admin_app.client.post(
        "/runs", content=run_spec_bytes(), headers=as_subject(ADMIN_SUBJECT)
    ).json()["id"]
    token = admin_app.runner.requests[run_id].run_token
    headers = {**as_subject(ADMIN_SUBJECT), "Authorization": f"Bearer {token}"}
    response = admin_app.client.post(f"/runs/{run_id}/events", content=b"{}", headers=headers)
    assert (response.status_code, code_of(response)) == (400, "forward_header_on_job_request")
