"""POST /specs/validate and GET /workload-presets: bench as a pure function (api/CLAUDE.md).

The rules (the schema, transport modes, stage lengths against the scrape
interval) and the workload preset catalog live in bench; this module hands it
the submitted text, or asks for the catalog, and returns its JSON. It is pure
computation, so it runs synchronously.
"""

import json
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path

from pydantic import TypeAdapter

from servtuner_api.cli_contract import error_message
from servtuner_api.models import KnobCatalog, SearchSpaceTemplate, WorkloadPreset
from servtuner_api.models.generated.knob_catalog_schema import Server as KnobServer

VALIDATE_TIMEOUT_SECONDS = 15.0
# bench's exit codes (bench/internal/cli): 0 valid, 2 invalid; both print the report.
REPORTING_EXIT_CODES = (0, 2)
# Larger RunSpecs are not plausible and would only be a way to load the api.
MAX_SPEC_BYTES = 1_048_576  # 1 MiB


class ValidatorUnavailableError(RuntimeError):
    """The api has no bench binary to validate with."""


@dataclass(frozen=True)
class ValidationReport:
    valid: bool
    errors: list[str]
    warnings: list[str]
    name: str | None = None


PRESETS_ADAPTER = TypeAdapter(list[WorkloadPreset])
SPACES_ADAPTER = TypeAdapter(list[SearchSpaceTemplate])
KNOBS_ADAPTER = TypeAdapter(KnobCatalog)


class SpecValidator:
    def __init__(self, bench_binary: Path | None) -> None:
        self._bench_binary = bench_binary
        # The catalogs are embedded in the binary and immutable, so one read of each lasts.
        self._presets: list[WorkloadPreset] | None = None
        self._spaces: list[SearchSpaceTemplate] | None = None
        self._knobs: dict[str, KnobCatalog] = {}

    def workload_presets(self) -> list[WorkloadPreset]:
        """The workload presets built into bench (``servtuner presets``), read once."""
        if self._presets is None:
            self._presets = PRESETS_ADAPTER.validate_json(self._catalog("presets"))
        return self._presets

    def search_space_templates(self) -> list[SearchSpaceTemplate]:
        """The starting search spaces built into bench (``servtuner spaces``), read once."""
        if self._spaces is None:
            self._spaces = SPACES_ADAPTER.validate_json(self._catalog("spaces"))
        return self._spaces

    def knob_catalog(self, server: KnobServer = KnobServer.vllm) -> KnobCatalog:
        """Every flag of a model server as a knob, from the server's source
        (``servtuner knobs --server <server>``), read once per server."""
        if server.value not in self._knobs:
            self._knobs[server.value] = KNOBS_ADAPTER.validate_json(
                self._catalog("knobs", "--server", server.value)
            )
        return self._knobs[server.value]

    def _catalog(self, command: str, *arguments: str) -> str:
        if self._bench_binary is None:
            raise ValidatorUnavailableError(
                f"SERVTUNER_BENCH_BINARY is not set: there is no bench to read {command} from"
            )
        completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
            [str(self._bench_binary), command, *arguments],
            capture_output=True,
            text=True,
            timeout=VALIDATE_TIMEOUT_SECONDS,
            check=True,
        )
        return completed.stdout

    def validate(self, spec_text: bytes) -> ValidationReport:
        """Validate a RunSpec given as YAML or JSON text."""
        if self._bench_binary is None:
            raise ValidatorUnavailableError(
                "SERVTUNER_BENCH_BINARY is not set: there is no bench to validate with"
            )
        if len(spec_text) > MAX_SPEC_BYTES:
            return ValidationReport(False, [f"the RunSpec is over {MAX_SPEC_BYTES} bytes"], [])
        with tempfile.TemporaryDirectory(prefix="servtuner-validate-") as directory:
            path = Path(directory) / "spec.yaml"  # bench reads YAML, and JSON is YAML
            path.write_bytes(spec_text)
            completed = subprocess.run(  # noqa: S603 - the binary path is operator configuration
                [str(self._bench_binary), "validate", "--json", "--spec", str(path)],
                capture_output=True,
                text=True,
                timeout=VALIDATE_TIMEOUT_SECONDS,
                check=False,
            )
        if completed.returncode not in REPORTING_EXIT_CODES or not completed.stdout.strip():
            message = completed.stderr.strip().splitlines()[-1:] or ["servtuner validate failed"]
            return ValidationReport(False, [error_message(message[0])], [])
        report = json.loads(completed.stdout)
        return ValidationReport(
            valid=bool(report["valid"]),
            errors=list(report.get("errors") or []),
            warnings=list(report.get("warnings") or []),
            name=report.get("name"),
        )
