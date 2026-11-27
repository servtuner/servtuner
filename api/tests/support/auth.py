"""Apps with user auth on: a setup token file, users made in the store."""

from collections.abc import Callable, Iterator
from contextlib import contextmanager
from dataclasses import dataclass, field, replace
from datetime import UTC, datetime, timedelta
from pathlib import Path

from fastapi.testclient import TestClient
from sqlalchemy.engine import Engine

from servtuner_api.app import create_app
from servtuner_api.auth.passwords import hash_password
from servtuner_api.auth.principal import Role
from servtuner_api.auth.service import AuthService
from servtuner_api.auth.store import AuthStore
from servtuner_api.database import UserRow, create_session_factory
from servtuner_api.ids import new_ulid
from servtuner_api.settings.models import (
    AuthSettings,
    ForwardHeaderSettings,
    LoginMethod,
    Provisioning,
)
from tests.conftest import build_service
from tests.support.fake_runner import FakeRunner

SETUP_TOKEN = "setup-token-used-only-in-tests"
PASSWORD = "correct horse battery"
# The cookie is Secure, so the client must speak https for it to be sent back.
BASE_URL = "https://testserver"


@dataclass
class Clock:
    """A clock tests move by hand."""

    now: datetime = field(default_factory=lambda: datetime(2026, 11, 26, 12, tzinfo=UTC))

    def __call__(self) -> datetime:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += timedelta(seconds=seconds)


@dataclass
class AuthApp:
    client: TestClient
    store: AuthStore
    clock: Clock
    runner: FakeRunner

    def add_user(
        self,
        username: str,
        role: Role,
        password: str | None = PASSWORD,
        **fields: object,
    ) -> UserRow:
        values: dict[str, object] = {
            "must_change_password": False,
            "password_hash": hash_password(password) if password else None,
            **fields,
        }
        user = UserRow(
            id=new_ulid(), username=username, role=role.value, created_at=self.clock(), **values
        )
        return self.store.insert_user(user)

    def login(self, username: str, password: str = PASSWORD) -> TestClient:
        response = self.client.post(
            "/auth/login", json={"username": username, "password": password}
        )
        assert response.status_code == 200, response.text
        return self.client


def auth_settings(tmp_path: Path, **fields: object) -> AuthSettings:
    token_file = tmp_path / "setup-token"
    token_file.write_text(SETUP_TOKEN + "\n")
    forward = fields.pop("forward_header", ForwardHeaderSettings())
    settings = AuthSettings(setup_token_file=token_file, forward_header=forward)  # type: ignore[arg-type]
    return replace(settings, **fields)  # type: ignore[arg-type]


FORWARD_ONLY = (LoginMethod.FORWARD_HEADER,)
BOTH_METHODS = (LoginMethod.FORWARD_HEADER, LoginMethod.LOCAL)
VIEWER_PROVISIONING = ForwardHeaderSettings(provisioning=Provisioning.VIEWER)


@contextmanager
def auth_app(
    engine: Engine,
    data_dir: Path,
    settings: AuthSettings,
    store_factory: Callable[[Engine], AuthStore] = lambda engine: AuthStore(
        create_session_factory(engine)
    ),
) -> Iterator[AuthApp]:
    clock = Clock()
    runner = FakeRunner()
    store = store_factory(engine)
    app = create_app(
        lambda: build_service(engine, runner, data_dir),
        lambda: AuthService(settings, store, clock),
    )
    with TestClient(app, base_url=BASE_URL) as client:
        yield AuthApp(client, store, clock, runner)
