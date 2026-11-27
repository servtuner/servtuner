"""A finished run's run_metrics rows, read from its bundle (api/CLAUDE.md; naming in indexing.py).

Reading is forgiving on purpose: a missing or unreadable file is logged and yields nothing, since
indexing must never change a run's outcome.
"""

import logging
from typing import Any

from servtuner_api.bundles import BundleStore, bundle_key, read_bundle_json
from servtuner_api.database import RunRow
from servtuner_api.errors import ForeignStoreError
from servtuner_api.indexing import ANALYSIS_FILE, CAPACITY_FILE, MetricRow, metric_rows
from servtuner_api.models import validate

logger = logging.getLogger(__name__)


def _bundle_document(
    store: BundleStore, run: RunRow, path: str, schema: str
) -> dict[str, Any] | None:
    """A schema-valid bundle document, or None when it is missing or unreadable (logged)."""
    try:
        document = read_bundle_json(store, bundle_key(store, run), path)
        if document is None:
            return None
        validate(schema, document)
    except ValueError, OSError, ForeignStoreError:
        logger.warning("run %s has an unreadable %s", run.id, path, exc_info=True)
        return None
    return document if isinstance(document, dict) else None


def run_metric_rows(store: BundleStore, run: RunRow) -> list[MetricRow] | None:
    """The run's rows from analysis.json, with cost rows from capacity.json when it has one;
    None when there is no readable analysis.json."""
    analysis = _bundle_document(store, run, ANALYSIS_FILE, "analysis")
    if analysis is None:
        return None
    return metric_rows(analysis, _bundle_document(store, run, CAPACITY_FILE, "capacity"))
