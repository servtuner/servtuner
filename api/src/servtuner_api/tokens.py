"""Per-run tokens for bench's status events (required in both modes).

Tokens are random, stored only as a SHA-256 hash, expire, and are compared in
constant time. A token is scoped to one run because the hash lives on its row,
and minted when the run's job launches, so a run that never launched has none.
"""

import hashlib
import hmac
import secrets
from datetime import UTC, datetime, timedelta

TOKEN_BYTES = 32


def new_run_token() -> str:
    return secrets.token_urlsafe(TOKEN_BYTES)


def hash_run_token(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()


def run_token_matches(token: str, stored_hash: str | None, expires_at: datetime | None) -> bool:
    """Compare in constant time, and refuse an expired token even if the hash matches. A run
    with no token (not launched yet) matches nothing."""
    if stored_hash is None or expires_at is None:
        return False
    hash_matches = hmac.compare_digest(hash_run_token(token), stored_hash)
    return hash_matches and datetime.now(UTC) < expires_at


def token_expiry(ttl_seconds: float) -> datetime:
    return datetime.now(UTC) + timedelta(seconds=ttl_seconds)
