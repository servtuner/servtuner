"""Follow-up runs: the RunSpec that tests one knob recommendation, and the
target patch the user applies before it runs."""

import json
import re

from servtuner_api.bundles import BundleStore, bundle_key
from servtuner_api.database import RunRow
from servtuner_api.errors import FollowupError
from servtuner_api.models import Recommendations
from servtuner_api.models.generated.bundle.recommendations_schema import Recommendation

# The target config patches bench writes, in the order a follow-up shows one.
TARGET_PATCHES = ("patches/bifrost.yaml", "patches/vllm.yaml", "patches/litellm.yaml")


def followup_experiment(run_name: str, knob: str) -> str:
    """The experiment of follow-up runs whose source run has none: ``<run name>-<knob>``."""
    return f"{run_name}-{re.sub(r'[^a-z0-9]+', '-', knob.lower()).strip('-')}"


def followup_document(
    run: RunRow,
    recommendations: Recommendations | None,
    recommendation_id: str,
    experiment: str | None,
) -> tuple[dict[str, object], Recommendation]:
    """The run's RunSpec with only the recommended knob changed, in one experiment so the
    follow-ups can be compared: ``experiment``, else the source run's, else
    ``<run name>-<knob>``. FollowupError says why there is nothing to run."""
    if recommendations is None:
        raise FollowupError(f"run {run.id} has no recommendations.json yet")
    found = [r for r in recommendations.recommendations if r.id == recommendation_id]
    if not found:
        raise FollowupError(f"run {run.id} has no recommendation {recommendation_id!r}")
    recommendation = found[0]
    value = recommendation.recommended.root if recommendation.recommended else None
    if recommendation.subject.kind.value != "knob" or value is None:
        raise FollowupError(
            f"{recommendation_id} is not a knob change with a value: there is nothing to run"
        )
    document = json.loads(run.spec_raw)
    if not isinstance(document, dict):
        raise FollowupError(f"run {run.id}'s RunSpec is not an object")
    knob = recommendation.subject.name
    document["knobs"] = {**document.get("knobs", {}), knob: value}
    document["experiment"] = (
        experiment or document.get("experiment") or followup_experiment(run.name, knob)
    )
    document["name"] = f"{document['name']}-followup"[:63]
    return document, recommendation


def target_patch(store: BundleStore, run: RunRow) -> str | None:
    """The first target config patch bench wrote for the run, as text."""
    for path in TARGET_PATCHES:
        content = store.get(bundle_key(store, run), path)
        if content is not None:
            return content.decode()
    return None
