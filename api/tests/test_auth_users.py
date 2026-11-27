"""The admin's users API, the audit log and who created what."""

from collections.abc import Iterator
from pathlib import Path

import pytest
from sqlalchemy.engine import Engine

from servtuner_api.auth.principal import Role
from tests.support.auth import BOTH_METHODS, PASSWORD, SETUP_TOKEN, AuthApp, auth_app, auth_settings
from tests.support.specs import run_spec_bytes

SETUP = {"X-Servtuner-Setup-Token": SETUP_TOKEN}
ADMIN = {"username": "first-admin", "password": PASSWORD}
NEW_PASSWORD = "a brand new passphrase"


@pytest.fixture
def admin_app(engine: Engine, data_dir: Path, tmp_path: Path) -> Iterator[AuthApp]:
    with auth_app(engine, data_dir, auth_settings(tmp_path, methods=BOTH_METHODS)) as app:
        assert app.client.post("/auth/setup", json=ADMIN, headers=SETUP).status_code == 201
        yield app


def code_of(response: object) -> str:
    return str(response.json()["detail"]["code"])  # type: ignore[attr-defined]


def admin_id(app: AuthApp) -> str:
    return str(app.client.get("/auth/me").json()["id"])


def test_an_admin_creates_a_local_user_who_must_change_the_password(admin_app: AuthApp) -> None:
    body = {"username": "ops-one", "role": "operator", "password": PASSWORD}
    created = admin_app.client.post("/users", json=body)
    assert created.status_code == 201, created.text
    view = created.json()
    assert (view["role"], view["has_password"], view["must_change_password"]) == (
        "operator",
        True,
        True,
    )
    admin_app.client.post("/auth/logout")
    client = admin_app.login("ops-one")
    assert code_of(client.get("/runs")) == "password_change_required"


def test_a_user_needs_a_way_to_sign_in(admin_app: AuthApp) -> None:
    response = admin_app.client.post("/users", json={"username": "nobody", "role": "viewer"})
    assert (response.status_code, code_of(response)) == (422, "no_way_to_sign_in")


def test_usernames_and_subjects_are_unique(admin_app: AuthApp) -> None:
    body = {"username": "dup", "role": "viewer", "subjects": ["dup@example.com"]}
    assert admin_app.client.post("/users", json=body).status_code == 201
    again = admin_app.client.post("/users", json={**body, "username": "DUP"})
    assert again.status_code == 409
    other = admin_app.client.post("/users", json={**body, "username": "other"})
    assert other.status_code == 409


def test_subjects_are_linked_and_unlinked(admin_app: AuthApp) -> None:
    body = {"username": "proxy-user", "role": "viewer", "subjects": ["CN=P U,O=Example"]}
    user = admin_app.client.post("/users", json=body).json()
    assert [identity["subject"] for identity in user["identities"]] == ["CN=P U,O=Example"]
    headers = {"X-Forwarded-User": "CN=P U,O=Example"}
    assert admin_app.client.get("/auth/me", headers=headers).json()["username"] == "proxy-user"
    linked = admin_app.client.post(
        f"/users/{user['id']}/identities", json={"subject": "pu@example.com"}
    )
    assert len(linked.json()["identities"]) == 2
    unlinked = admin_app.client.delete(
        f"/users/{user['id']}/identities", params={"subject": "CN=P U,O=Example"}
    )
    assert [identity["subject"] for identity in unlinked.json()["identities"]] == ["pu@example.com"]
    assert admin_app.client.get("/auth/me", headers=headers).status_code == 403


def test_a_role_change_signs_the_user_out(
    admin_app: AuthApp, engine: Engine, data_dir: Path, tmp_path: Path
) -> None:
    user = admin_app.add_user("promoted", Role.VIEWER)
    with auth_app(engine, data_dir, auth_settings(tmp_path, methods=BOTH_METHODS)) as other:
        other.clock.now = admin_app.clock.now
        other.login("promoted")
        assert other.client.post("/runs", content=run_spec_bytes()).status_code == 403
        response = admin_app.client.patch(f"/users/{user.id}", json={"role": "operator"})
        assert response.json()["role"] == "operator"
        assert other.client.get("/runs").status_code == 401
        other.login("promoted")
        assert other.client.post("/runs", content=run_spec_bytes()).status_code == 201


def test_the_last_admin_cannot_be_demoted_or_disabled(admin_app: AuthApp) -> None:
    me = admin_id(admin_app)
    demote = admin_app.client.patch(f"/users/{me}", json={"role": "viewer"})
    assert (demote.status_code, code_of(demote)) == (409, "last_admin")
    disable = admin_app.client.patch(f"/users/{me}", json={"is_disabled": True})
    assert disable.status_code == 409
    assert admin_app.client.get("/runs").status_code == 200


def test_disabling_and_enabling(admin_app: AuthApp) -> None:
    user = admin_app.add_user("leaver", Role.OPERATOR)
    disabled = admin_app.client.patch(f"/users/{user.id}", json={"is_disabled": True}).json()
    assert disabled["is_disabled"] is True
    enabled = admin_app.client.patch(f"/users/{user.id}", json={"is_disabled": False}).json()
    assert enabled["is_disabled"] is False


def test_a_reset_gives_a_temporary_password_once(admin_app: AuthApp) -> None:
    user = admin_app.add_user("forgetful", Role.VIEWER)
    reset = admin_app.client.post(f"/users/{user.id}/password").json()
    admin_app.client.post("/auth/logout")
    client = admin_app.login("forgetful", reset["password"])
    assert code_of(client.get("/runs")) == "password_change_required"
    body = {"current_password": reset["password"], "new_password": NEW_PASSWORD}
    assert client.post("/auth/password", json=body).status_code == 204


def test_only_admins_reach_users_and_audit(admin_app: AuthApp) -> None:
    admin_app.add_user("ops", Role.OPERATOR)
    admin_app.client.post("/auth/logout")
    client = admin_app.login("ops")
    assert client.get("/users").status_code == 403
    assert client.get("/audit").status_code == 403


def test_the_audit_log_pages_newest_first(admin_app: AuthApp) -> None:
    for name in ("a-user", "b-user", "c-user"):
        admin_app.client.post(
            "/users", json={"username": name, "role": "viewer", "password": PASSWORD}
        )
    first = admin_app.client.get("/audit", params={"limit": 2}).json()
    assert [event["action"] for event in first] == ["user_create", "user_create"]
    rest = admin_app.client.get("/audit", params={"before_id": first[-1]["id"]}).json()
    assert all(event["id"] < first[-1]["id"] for event in rest)
    assert {"setup", "user_create"} <= {event["action"] for event in rest}
    assert all("password" not in str(event["detail"]) for event in first + rest)


def test_runs_record_who_created_them(admin_app: AuthApp) -> None:
    created = admin_app.client.post("/runs", content=run_spec_bytes(experiment="by-whom")).json()
    assert created["created_by"] == admin_id(admin_app)
    assert created["created_by_username"] == "first-admin"
    listed = admin_app.client.get("/runs").json()[0]
    assert listed["created_by_username"] == "first-admin"


def test_mode_none_runs_have_no_creator(client: object) -> None:
    created = client.post("/runs", content=run_spec_bytes()).json()  # type: ignore[attr-defined]
    assert "created_by" not in created or created["created_by"] is None
