"""Password hashing: Argon2id now, scrypt hashes upgraded at login."""

import base64
import hashlib
import secrets
from pathlib import Path

from sqlalchemy.engine import Engine

from servtuner_api.auth.passwords import (
    ARGON2_PREFIX,
    DUMMY_HASH,
    hash_password,
    needs_rehash,
    verify_password,
)
from servtuner_api.auth.principal import Role
from tests.support.auth import PASSWORD, auth_app, auth_settings


def encode(raw: bytes) -> str:
    return base64.b64encode(raw).decode().rstrip("=")


def scrypt_hash(password: str) -> str:
    """A hash as the first build wrote it: scrypt N=2^14, r=8, p=5."""
    salt = secrets.token_bytes(16)
    key = hashlib.scrypt(password.encode(), salt=salt, n=2**14, r=8, p=5, dklen=32)
    return f"$scrypt$ln=14,r=8,p=5${encode(salt)}${encode(key)}"


def test_new_hashes_are_argon2id() -> None:
    stored = hash_password(PASSWORD)
    assert stored.startswith(ARGON2_PREFIX) and "m=19456,t=2,p=1" in stored
    assert verify_password(PASSWORD, stored)
    assert not verify_password("not the password", stored)
    assert not needs_rehash(stored)
    assert DUMMY_HASH.startswith(ARGON2_PREFIX)


def test_scrypt_hashes_still_verify_and_ask_for_a_rehash() -> None:
    stored = scrypt_hash(PASSWORD)
    assert verify_password(PASSWORD, stored)
    assert not verify_password("not the password", stored)
    assert needs_rehash(stored)


def test_malformed_and_costly_hashes_match_nothing() -> None:
    for stored in (
        "",
        "plain",
        "$scrypt$ln=x$a$b",
        "$argon2id$garbage",
        "$scrypt$ln=30,r=8,p=1$AA$AA",
    ):
        assert not verify_password(PASSWORD, stored)
        assert needs_rehash(stored)


def test_a_login_replaces_a_scrypt_hash(engine: Engine, data_dir: Path, tmp_path: Path) -> None:
    with auth_app(engine, data_dir, auth_settings(tmp_path)) as app:
        user = app.add_user(
            "old-hash", Role.ADMIN, password=None, password_hash=scrypt_hash(PASSWORD)
        )
        app.login("old-hash")
        upgraded = app.store.user(user.id)
        assert upgraded is not None and upgraded.password_hash is not None
        assert upgraded.password_hash.startswith(ARGON2_PREFIX)
        assert verify_password(PASSWORD, upgraded.password_hash)
