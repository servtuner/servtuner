"""A Runner for tests: records launches, and finishes runs when the test says so."""

from collections.abc import Iterator

from servtuner_api.runner import LaunchRequest, RunHandle, RunnerState, RunnerStatus


class FakeRunner:
    def __init__(self) -> None:
        self.requests: dict[str, LaunchRequest] = {}
        self.cancelled: list[str] = []
        self.launch_error: Exception | None = None
        self._statuses: dict[str, RunnerStatus] = {}

    def launch(self, request: LaunchRequest) -> RunHandle:
        if self.launch_error is not None:
            raise self.launch_error
        self.requests[request.run_id] = request
        return RunHandle(request.run_id, "fake")

    def status(self, handle: RunHandle) -> RunnerStatus:
        return self._statuses.get(handle.run_id, RunnerStatus(RunnerState.RUNNING))

    def cancel(self, handle: RunHandle) -> None:
        self.cancelled.append(handle.run_id)

    def logs(self, handle: RunHandle) -> Iterator[str]:
        yield from (f"log of {handle.run_id}",)

    def finish(self, run_id: str, state: RunnerState, exit_code: int | None = None) -> None:
        self._statuses[run_id] = RunnerStatus(state, exit_code=exit_code)

    def only_token(self) -> tuple[str, str]:
        """The (run id, token) of the single launched run."""
        (request,) = self.requests.values()
        return request.run_id, request.run_token
