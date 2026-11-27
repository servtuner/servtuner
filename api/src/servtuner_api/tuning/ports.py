"""What a tuning session needs of the rest of the api, as protocols (the service passes itself
and its sub-services).
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any, Protocol

from servtuner_api.database import ManagedTargetRow, RunRow
from servtuner_api.managed_targets.views import ManagedTargetResource
from servtuner_api.records.base import RunFilters
from servtuner_api.resources.experiments import ExperimentAnalysisResource, ExperimentAnalyze

if TYPE_CHECKING:  # managed_targets imports from tuning
    from servtuner_api.managed_targets.views import BootProbeView, ReadinessView


class ManagedOps(Protocol):
    """What a session needs of the managed targets (managed_targets.ManagedTargetService)."""

    def get(self, target_id: str) -> ManagedTargetRow: ...

    def deploy(self, target_id: str, raw: bytes) -> ManagedTargetRow: ...

    def readiness(self, target_id: str) -> ReadinessView: ...

    def boot_probe(self, target_id: str, raw: bytes) -> BootProbeView: ...

    def scale_down(self, target_id: str) -> None: ...

    def knob_args(self, target_id: str, knobs: Mapping[str, Any]) -> list[str]: ...

    def installed_vllm_version(self, target_id: str) -> str | None: ...

    def resource(self, row: ManagedTargetRow) -> ManagedTargetResource: ...


class ExperimentAnalyzer(Protocol):
    """What a session needs of the experiments (experiments.ExperimentService)."""

    def analyze(
        self, name: str, request: ExperimentAnalyze
    ) -> ExperimentAnalysisResource | None: ...


class RunOps(Protocol):
    """What a session needs of the runs (service.RunService)."""

    @property
    def experiments(self) -> ExperimentAnalyzer: ...

    def create_run(self, spec_raw: bytes) -> RunRow: ...

    def get_run(self, run_id: str) -> RunRow | None: ...

    def bundle_key(self, run: RunRow) -> str: ...

    def list_runs(self, filters: RunFilters, limit: int) -> list[RunRow]: ...

    def cancel_run(self, run_id: str) -> RunRow | None: ...
