"""Password hashing and policy.

New hashes are Argon2id (argon2-cffi). Hashes written before it, scrypt from the standard
library, still verify, and ``needs_rehash`` says so, so each is replaced by Argon2id at its
user's next successful login.
"""

import base64
import hashlib
import hmac
import secrets

from argon2 import PasswordHasher
from argon2.exceptions import InvalidHashError, VerificationError, VerifyMismatchError

# OWASP's Argon2id minimum: 19 MiB of memory, 2 passes, 1 lane.
ARGON2_MEMORY_KIB = 19456
ARGON2_TIME_COST = 2
ARGON2_PARALLELISM = 1
ARGON2_PREFIX = "$argon2id$"
_HASHER = PasswordHasher(
    time_cost=ARGON2_TIME_COST, memory_cost=ARGON2_MEMORY_KIB, parallelism=ARGON2_PARALLELISM
)

# The earlier scheme, kept only to verify hashes written before Argon2id.
SCRYPT_ALGORITHM = "scrypt"
SCRYPT_MAX_LOG2_COST = 16  # refuse a stored row that would make one login cost far more memory
# scrypt needs 128 * N * r bytes plus slack; 64 MiB covers N=2^16, r=8 with room.
SCRYPT_MAX_MEMORY_BYTES = 64 * 1024 * 1024

MIN_PASSWORD_LENGTH = 12  # NIST SP 800-63B: length, no composition rules
MAX_PASSWORD_LENGTH = 256


class PasswordPolicyError(ValueError):
    pass


def check_policy(password: str) -> None:
    if len(password) < MIN_PASSWORD_LENGTH:
        raise PasswordPolicyError(f"a password needs at least {MIN_PASSWORD_LENGTH} characters")
    if len(password) > MAX_PASSWORD_LENGTH:
        raise PasswordPolicyError(f"a password has at most {MAX_PASSWORD_LENGTH} characters")


def hash_password(password: str) -> str:
    """``$argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>``."""
    return _HASHER.hash(password)


def verify_password(password: str, stored: str) -> bool:
    """Constant-time; a malformed or unknown stored hash matches nothing."""
    if stored.startswith(ARGON2_PREFIX):
        try:
            return _HASHER.verify(stored, password)
        except VerifyMismatchError, VerificationError, InvalidHashError:
            return False
    return _verify_scrypt(password, stored)


def needs_rehash(stored: str) -> bool:
    """True for anything but an Argon2id hash with today's parameters."""
    if not stored.startswith(ARGON2_PREFIX):
        return True
    try:
        return _HASHER.check_needs_rehash(stored)
    except InvalidHashError:
        return True


def _decode(text: str) -> bytes:
    return base64.b64decode(text + "=" * (-len(text) % 4))


def _verify_scrypt(password: str, stored: str) -> bool:
    """``$scrypt$ln=<log2 N>,r=<r>,p=<p>$<salt>$<key>``, base64 without padding."""
    parts = stored.split("$")
    if len(parts) != 5 or parts[0] or parts[1] != SCRYPT_ALGORITHM:
        return False
    try:
        parameters = {
            name: int(value)
            for name, value in (field.split("=", 1) for field in parts[2].split(","))
        }
        salt, key = _decode(parts[3]), _decode(parts[4])
        if parameters["ln"] > SCRYPT_MAX_LOG2_COST:
            return False
        derived = hashlib.scrypt(
            password.encode(),
            salt=salt,
            n=2 ** parameters["ln"],
            r=parameters["r"],
            p=parameters["p"],
            maxmem=SCRYPT_MAX_MEMORY_BYTES,
            dklen=len(key),
        )
    except KeyError, ValueError:
        return False
    return hmac.compare_digest(derived, key)


# Verified when the user is unknown or has no password, so every failure costs one Argon2id.
DUMMY_HASH = hash_password(secrets.token_urlsafe(16))
