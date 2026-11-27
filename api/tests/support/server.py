"""Run the real uvicorn ``ApiServer`` on an ephemeral port for TLS tests."""

import threading
from collections.abc import Generator
from contextlib import contextmanager
from dataclasses import dataclass

from servtuner_api.server import ApiServer, build_app
from servtuner_api.settings.models import Settings
from tests.support.certs import LOOPBACK
from tests.support.waiting import wait_until


@dataclass(frozen=True)
class RunningServer:
    port: int

    def url(self, scheme: str, path: str = "/healthz") -> str:
        return f"{scheme}://localhost:{self.port}{path}"


@contextmanager
def running_server(settings: Settings) -> Generator[RunningServer]:
    api_server = ApiServer(build_app(settings, migrate=False), settings, host=LOOPBACK, port=0)
    thread = threading.Thread(target=api_server.run, daemon=True)
    thread.start()
    wait_until(lambda: api_server.server.started, "the api server to start")
    (listener,) = api_server.server.servers[0].sockets
    try:
        yield RunningServer(listener.getsockname()[1])
    finally:
        api_server.server.should_exit = True
        thread.join()
