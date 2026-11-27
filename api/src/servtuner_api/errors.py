"""The errors services raise and routes map to HTTP; one module, so neither side imports the
other's internals."""


class ScheduleBusyError(RuntimeError):
    """The schedule's last run has not finished, so another would overlap it."""


class InvalidRunTokenError(PermissionError):
    """Unknown run, wrong token, or expired token. One error so callers learn nothing."""


class ForeignStoreError(LookupError):
    """A run's bundle is in a store this api is not configured for."""


class InvalidBundleFileError(ValueError):
    """A bundle file exists but breaks its schema."""

    def __init__(self, path: str, violations: list[str]) -> None:
        self.path = path
        self.violations = violations
        super().__init__(f"{path} breaks its schema: {'; '.join(violations[:3])}")


class NotAwaitingError(ValueError):
    """The run is not waiting for a confirmed config change."""


class FollowupError(ValueError):
    """The recommendation cannot be followed up with runs; the message says why."""


class NoAnalysableRunsError(LookupError):
    """The experiment has no succeeded, analysed run to compare."""


class UnknownRunsError(LookupError):
    """A request named runs the api does not know."""

    def __init__(self, run_ids: list[str]) -> None:
        self.run_ids = run_ids
        super().__init__(f"unknown runs: {', '.join(run_ids)}")


class InvalidDocumentError(ValueError):
    """The body is not JSON or breaks its schema; ``violations`` lists every problem."""

    def __init__(self, violations: list[str]) -> None:
        self.violations = violations
        super().__init__("; ".join(violations))
