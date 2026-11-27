"""ULID generation for run ids (root CLAUDE.md: a run is identified by a ULID).

Written against the ULID spec (48-bit millisecond timestamp, 80 random bits,
Crockford base32) rather than adding a dependency for twenty lines.
"""

import os
import re
import time

_CROCKFORD_ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
_ULID_LENGTH = 26
ULID_PATTERN = re.compile(r"^[0-7][0-9A-HJKMNP-TV-Z]{25}$")


def new_ulid(now_milliseconds: int | None = None) -> str:
    """Return a new ULID; ids created later sort after ids created earlier."""
    timestamp = time.time_ns() // 1_000_000 if now_milliseconds is None else now_milliseconds
    value = (timestamp << 80) | int.from_bytes(os.urandom(10))
    characters = []
    for _ in range(_ULID_LENGTH):
        value, index = divmod(value, 32)
        characters.append(_CROCKFORD_ALPHABET[index])
    return "".join(reversed(characters))
