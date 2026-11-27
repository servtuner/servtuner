"""Request bodies as documents: JSON parsed with one error shape, timestamps in UTC."""

import json
from datetime import UTC, datetime

from servtuner_api.errors import InvalidDocumentError


def parse_json_document(raw: bytes) -> object:
    try:
        return json.loads(raw)
    except ValueError as error:
        raise InvalidDocumentError([f"body is not valid JSON: {error}"]) from error


def parse_timestamp(timestamp: str) -> datetime:
    return datetime.fromisoformat(timestamp).astimezone(UTC)
