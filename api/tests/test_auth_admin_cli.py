"""servtuner-api admin: recovery from inside the pod."""

import io
from pathlib import Path

from sqlalchemy.engine import Engine

from servtuner_api.auth.cli import run_admin_command
from servtuner_api.auth.principal import Role
from tests.support.auth import auth_app, auth_settings


def test_reset_password_forces_a_change(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as app:
        app.add_user("admin-one", Role.ADMIN)
        out = io.StringIO()
        assert run_admin_command(["reset-password", "admin-one"], app.store, out) == 0
        password = out.getvalue().split(": ", 1)[1].split()[0]
        client = app.login("admin-one", password)
        response = client.get("/runs")
        assert response.json()["detail"]["code"] == "password_change_required"


def test_set_role_never_leaves_no_admin(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as app:
        app.add_user("only-admin", Role.ADMIN)
        app.add_user("helper", Role.VIEWER)
        out = io.StringIO()
        assert run_admin_command(["set-role", "only-admin", "viewer"], app.store, out) == 1
        assert run_admin_command(["set-role", "helper", "admin"], app.store, out) == 0
        assert run_admin_command(["set-role", "only-admin", "viewer"], app.store, out) == 0
        assert app.store.user_by_username("helper").role == "admin"  # type: ignore[union-attr]


def test_an_unknown_user_is_an_error(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as app:
        assert run_admin_command(["enable", "ghost"], app.store, io.StringIO()) == 1
