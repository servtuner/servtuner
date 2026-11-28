"""Hosts outside Kubernetes, through servtuner launcher."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any, Literal, override

import httpx2

from servtuner_api.deployer.base import (
    HTTP_BAD_REQUEST,
    HTTP_NOT_FOUND,
    HTTP_OK,
    AnySpec,
    BootProbes,
    Checker,
    DeployInputError,
    Readiness,
    VllmLog,
    host_spec,
    offline_env,
)
from servtuner_api.deployer.boot import LOG_TAIL_LINES, SIZING_LOG_TAIL_LINES, classify_boot_failure
from servtuner_api.managed_targets.runs import run_target
from servtuner_api.managed_targets.spec import HostTargetSpec
from servtuner_api.models.generated.host_machine_schema import HostMachine
from servtuner_api.secret_store import SecretStore

LAUNCHER_TIMEOUT_SECONDS = (
    60.0  # a PUT waits for the previous vLLM to stop (bench's StopGrace: 30 s)
)


class LauncherError(RuntimeError):
    """The launcher did not answer as expected: a network or host problem; sessions retry."""


def host_args(spec: HostTargetSpec, knob_args: list[str]) -> list[str]:
    """vllm serve's args on a host: the model, where it listens, the base flags, then the knobs."""
    args = [spec.model, "--host=0.0.0.0", f"--port={spec.port}"]
    if spec.served_model_name:
        args.append(f"--served-model-name={spec.served_model_name}")
    return args + spec.base_flags + knob_args


HTTP_UNAUTHORIZED = 401


HTTP_FORBIDDEN = 403


@dataclass(frozen=True)
class LauncherCheck:
    """Can the api work this host's launcher: reached, token accepted, and what vLLM is doing."""

    status: Literal["ok", "unreachable", "refused", "no_token", "error"]
    message: str
    vllm_state: str | None = None
    vllm_args: list[str] | None = None


@dataclass
class LauncherDeployer(BootProbes):
    """Deploys a host target through its ``servtuner launcher``: same interface as ``Deployer``."""

    render_knobs: Callable[[Mapping[str, Any], str], list[str]]
    checker: Checker
    store: SecretStore | None
    prometheus: dict[str, Any] | None = None
    offline: bool = False  # an install without internet access: offline_env
    # Tests pass an httpx2 transport; production uses the network.
    transport: Any = None

    def deploy(
        self, name: str, namespace: str, any_spec: AnySpec, knobs: Mapping[str, Any]
    ) -> list[str]:
        spec = host_spec(any_spec)
        args = host_args(spec, self.render_knobs(knobs, "vllm"))  # host targets run vLLM
        env = offline_env(spec.env, self.offline)
        response = self._call(
            spec, "PUT", "/server", json={"server": "vllm", "args": args, "env": env}
        )
        if response.status_code == HTTP_BAD_REQUEST:
            raise DeployInputError([f"the launcher refused the request: {response.text.strip()}"])
        self._expect_ok(response)
        return args

    @override
    def boot_log(self, name: str, namespace: str, any_spec: AnySpec) -> str:
        response = self._call(
            host_spec(any_spec), "GET", "/server/log", params={"tail": str(SIZING_LOG_TAIL_LINES)}
        )
        return response.text if response.status_code == HTTP_OK else ""

    def installed_vllm_version(self, any_spec: AnySpec) -> str | None:
        """The host's vLLM version before it boots (`vllm --version` through the launcher), or
        None when the launcher cannot say: unreachable, no token, or a launcher too old for it."""
        try:
            response = self._call(
                host_spec(any_spec), "GET", "/server/version", params={"server": "vllm"}
            )
        except LauncherError, DeployInputError:
            return None
        if response.status_code != HTTP_OK:
            return None
        version = response.json().get("version")
        return str(version) if version else None

    def machine(self, any_spec: AnySpec) -> HostMachine:
        """The host's GPUs, CPU and memory as its launcher reads them (GET /machine)."""
        response = self._call(host_spec(any_spec), "GET", "/machine")
        if response.status_code == HTTP_NOT_FOUND:
            raise DeployInputError(
                [
                    "the launcher is too old to report its machine: "
                    "update servtuner launcher on the host"
                ]
            )
        self._expect_ok(response)
        return HostMachine.model_validate(response.json())

    def check(self, any_spec: AnySpec) -> LauncherCheck:
        """One status call with the token, explained for a person (Check launcher)."""
        spec = host_spec(any_spec)
        try:
            response = self._call(spec, "GET", "/server")
        except DeployInputError as error:
            return LauncherCheck("no_token", "; ".join(error.problems))
        except LauncherError as error:
            return LauncherCheck("unreachable", str(error))
        if response.status_code in (HTTP_UNAUTHORIZED, HTTP_FORBIDDEN):
            return LauncherCheck(
                "refused", "the launcher refused the token: it is not the one it was started with"
            )
        if response.status_code != HTTP_OK:
            return LauncherCheck(
                "error",
                f"the launcher answered {response.status_code}: {response.text.strip()[:200]}",
            )
        status: dict[str, Any] = response.json()
        state = str(status.get("state", "unknown"))
        running = "running vLLM" if state == "running" else f"vLLM {state}"
        return LauncherCheck(
            "ok",
            f"reached, token accepted: {running}",
            vllm_state=state,
            vllm_args=status.get("args"),
        )

    @override
    def recent_log(self, name: str, namespace: str, any_spec: AnySpec, tail_lines: int) -> VllmLog:
        spec = host_spec(any_spec)
        response = self._call(spec, "GET", "/server/log", params={"tail": str(tail_lines)})
        self._expect_ok(response)
        return VllmLog(source=f"launcher {spec.launcher_url}", text=response.text)

    @override
    def poll(
        self, target_id: str, name: str, namespace: str, any_spec: AnySpec, args: list[str]
    ) -> Readiness:
        spec = host_spec(any_spec)
        response = self._call(spec, "GET", "/server")
        self._expect_ok(response)
        status: dict[str, Any] = response.json()
        state = status.get("state")
        if state == "none":
            return Readiness("failed", "the launcher has started nothing: deploy first")
        if state == "exited":
            exit_code = status.get("exit_code")
            if exit_code == 0:
                return Readiness("failed", "vLLM exited cleanly before it was ready")
            log = self._call(spec, "GET", "/server/log", params={"tail": str(LOG_TAIL_LINES)})
            self._expect_ok(log)
            failure = classify_boot_failure(log.text, exit_code, None)
            state_name = "infeasible" if failure.kind == "infeasible" else "failed"
            return Readiness(state_name, f"{failure.rule}: {failure.message}", failure=failure)
        if state != "running":
            return Readiness("failed", f"vLLM is {state} on the host")
        if status.get("args") != args:
            return Readiness(
                "failed", f"vLLM runs other args than rendered: {status.get('args')} != {args}"
            )
        request: dict[str, Any] = {"target": run_target(name, namespace, spec)}
        if self.prometheus is not None:
            request["prometheus"] = self.prometheus
        view = self.checker.check(request)
        failed = [step.name.value for step in view.result.checks if step.status.value == "failed"]
        if failed:
            return Readiness(
                "pending", f"check-target not passing yet: {', '.join(failed)}", check=view
            )
        return Readiness("ready", "running with the rendered args, check-target ok", check=view)

    def scale_to_zero(self, name: str, namespace: str, spec: AnySpec | None = None) -> None:
        """Free the GPU: stop the host's vLLM."""
        if spec is None:
            raise TypeError("a host target's scale-down needs its spec (the launcher's address)")
        self._expect_ok(self._call(host_spec(spec), "DELETE", "/server"))

    def teardown(self, name: str, namespace: str, any_spec: AnySpec, **_: Any) -> None:
        self.scale_to_zero(name, namespace, any_spec)

    def _call(self, spec: HostTargetSpec, method: str, path: str, **kwargs: Any) -> httpx2.Response:
        token = self._token(spec)
        with httpx2.Client(
            base_url=spec.launcher_url.rstrip("/"),
            headers={"Authorization": f"Bearer {token}"},
            timeout=LAUNCHER_TIMEOUT_SECONDS,
            follow_redirects=False,  # a redirect could carry the token elsewhere
            transport=self.transport,
        ) as client:
            try:
                return client.request(method, path, **kwargs)
            except httpx2.TransportError as error:
                raise LauncherError(
                    f"the launcher at {spec.launcher_url} is unreachable: {error}"
                ) from None

    def _token(self, spec: HostTargetSpec) -> str:
        if self.store is None:
            raise LauncherError("no secret store: the launcher token cannot be read")
        if spec.launcher_token is None:
            raise DeployInputError(["set the launcher token first (credentials/launcher_token)"])
        secret = self.store.read(spec.launcher_token.name) or {}
        token = secret.get(spec.launcher_token.key)
        if not token:
            raise DeployInputError(
                [
                    f"Secret {spec.launcher_token.name} has no key {spec.launcher_token.key} "
                    "(launcher_token)"
                ]
            )
        return token

    @staticmethod
    def _expect_ok(response: httpx2.Response) -> None:
        if response.status_code != HTTP_OK:
            raise LauncherError(
                f"the launcher answered {response.status_code}: {response.text.strip()[:200]}"
            )
