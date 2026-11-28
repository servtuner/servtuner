"""Storing, deploying and tearing down managed targets."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from datetime import UTC, datetime
from typing import TYPE_CHECKING, Any

from servtuner_api.database import ManagedTargetRow
from servtuner_api.ids import new_ulid
from servtuner_api.managed_targets.runs import (
    HOST_CREDENTIAL_FIELDS,
    credential_statuses,
    managed_credential_name,
    run_target,
)
from servtuner_api.managed_targets.spec import (
    HostTargetSpec,
    ManagedTargetSpec,
    SecretKeyRef,
    parse_spec,
)
from servtuner_api.managed_targets.views import (
    BootProbeRequest,
    BootProbeView,
    DeploymentView,
    DeployRequest,
    LauncherCheckView,
    LogView,
    ManagedTargetCreate,
    ManagedTargetResource,
    ReadinessView,
)
from servtuner_api.models.generated.boot_results_schema import TuningBootResult
from servtuner_api.models.generated.host_machine_schema import HostMachine
from servtuner_api.price_book import PriceBook, infra_pricing
from servtuner_api.records.repository import RunRepository
from servtuner_api.resources.targets import CredentialInput
from servtuner_api.secret_store import SecretStore
from servtuner_api.targets.common import (
    TOKEN_KEY,
    SecretStoreUnavailableError,
    TargetConflictError,
    TargetInputError,
    TargetNotFoundError,
    check_target_shape,
    parse_body,
)

if TYPE_CHECKING:
    from servtuner_api.deployer.kubernetes import Deployer
    from servtuner_api.deployer.launcher import LauncherDeployer


class DeployerUnavailableError(RuntimeError):
    """No deployer: managed targets are deployed only by the api running in Kubernetes."""


class ManagedTargetService:
    """Managed targets: CRUD confined to the granted namespaces, and deploys."""

    def __init__(
        self,
        repository: RunRepository,
        tuning_namespaces: tuple[str, ...],
        deployer: Deployer | None = None,
        clock: Callable[[], datetime] = lambda: datetime.now(UTC),
        host_deployer: LauncherDeployer | None = None,
        secret_store: SecretStore | None = None,
    ) -> None:
        self._repository = repository
        self._namespaces = tuning_namespaces
        self._deployer = deployer
        self._host_deployer = host_deployer
        self._secret_store = secret_store
        self._clock = clock

    def list_targets(self) -> list[ManagedTargetRow]:
        return self._repository.list_managed_targets()

    def get(self, target_id: str) -> ManagedTargetRow:
        row = self._repository.get_managed_target(target_id)
        if row is None:
            raise TargetNotFoundError(f"no managed target {target_id}")
        return row

    def create(self, raw: bytes) -> ManagedTargetRow:
        body = self._checked(raw, current_id=None)
        now = self._clock()
        row = ManagedTargetRow(
            id=new_ulid(),
            name=body.name,
            description=body.description,
            namespace=body.namespace,
            spec=body.spec.model_dump(mode="json", exclude_none=True),
            created_at=now,
            updated_at=now,
        )
        self._repository.insert_managed_target(row)
        return row

    def update(self, target_id: str, raw: bytes) -> ManagedTargetRow:
        self.get(target_id)
        body = self._checked(raw, current_id=target_id)
        updated = self._repository.update_managed_target(
            target_id,
            name=body.name,
            description=body.description,
            namespace=body.namespace,
            spec=body.spec.model_dump(mode="json", exclude_none=True),
            updated_at=self._clock(),
        )
        assert updated is not None
        return updated

    def delete(self, target_id: str) -> None:
        """Tear down what was deployed (the model cache stays), then forget the target."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        if row.deployment is not None:
            self._require_deployer(spec).teardown(row.name, row.namespace, spec)
        for slot in credential_statuses(row.id, spec):
            self._forget_credential(row.id, slot, spec)
        self._repository.delete_managed_target(target_id)

    def deploy(self, target_id: str, raw: bytes) -> ManagedTargetRow:
        """Apply the target with these knobs; readiness is polled separately."""
        row = self.get(target_id)
        body = parse_body(raw, DeployRequest)
        spec = parse_spec(row.spec)
        args = self._require_deployer(spec).deploy(row.name, row.namespace, spec, body.knobs)
        deployment = DeploymentView(knobs=body.knobs, args=args, deployed_at=self._clock())
        updated = self._repository.update_managed_target(
            target_id, deployment=deployment.model_dump(mode="json")
        )
        assert updated is not None
        return updated

    def readiness(self, target_id: str) -> ReadinessView:
        row = self.get(target_id)
        if row.deployment is None:
            return ReadinessView(state="failed", reason="not deployed yet")
        spec = parse_spec(row.spec)
        readiness = self._require_deployer(spec).poll(
            row.id, row.name, row.namespace, spec, list(row.deployment["args"])
        )
        return ReadinessView.of(readiness)

    def boot_probe(self, target_id: str, raw: bytes) -> BootProbeView:
        """Poll the deployed configuration as a boot probe: no load, only its KV capacity."""
        row = self.get(target_id)
        body = parse_body(raw, BootProbeRequest)
        if row.deployment is None:
            return BootProbeView(readiness=ReadinessView(state="failed", reason="not deployed yet"))
        spec = parse_spec(row.spec)
        readiness, result = self._require_deployer(spec).boot_result(
            row.id, row.name, row.namespace, spec, row.deployment, body.tokens_per_request
        )
        return BootProbeView(
            readiness=ReadinessView.of(readiness),
            result=None if result is None else TuningBootResult.model_validate(result),
        )

    def scale_down(self, target_id: str) -> None:
        """Free the GPU: the Deployment goes to 0 replicas (a host's vLLM stops); the rest stays."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        self._require_deployer(spec).scale_to_zero(row.name, row.namespace, spec)
        if row.deployment is not None:
            deployment = {**row.deployment, "scaled_down_at": self._clock().isoformat()}
            self._repository.update_managed_target(target_id, deployment=deployment)

    def logs(self, target_id: str, tail_lines: int) -> LogView:
        """The newest vLLM instance's last lines (a crash-looping pod's last exit included)."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        log = self._require_deployer(spec).recent_log(row.name, row.namespace, spec, tail_lines)
        return LogView(source=log.source, lines=log.text.splitlines())

    def installed_vllm_version(self, target_id: str) -> str | None:
        """A host target's vLLM version as its launcher reports it, before any boot; None for a
        Kubernetes target (its pinned image fixes the version) or when the launcher cannot say."""
        spec = parse_spec(self.get(target_id).spec)
        if not isinstance(spec, HostTargetSpec) or self._host_deployer is None:
            return None
        return self._host_deployer.installed_vllm_version(spec)

    def check_launcher(self, target_id: str) -> LauncherCheckView:
        """Reach the host's launcher with its token, before a deploy depends on it."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        if not isinstance(spec, HostTargetSpec):
            raise TargetInputError(["only a host target has a launcher"])
        check = self._require_host_deployer().check(spec)
        return LauncherCheckView(
            status=check.status,
            message=check.message,
            vllm_state=check.vllm_state,
            vllm_args=check.vllm_args,
        )

    def resource(self, row: ManagedTargetRow) -> ManagedTargetResource:
        """The resource, with a host target's host for its runs' hardware and prices, or the price
        book's prices for a Kubernetes target's runs."""
        host_id = row.spec.get("host_id")
        host = self._repository.get_host(host_id) if isinstance(host_id, str) else None
        book = self._repository.get_price_book()
        install_pricing = (
            None if book is None else infra_pricing(PriceBook.model_validate(book.book))
        )
        return ManagedTargetResource.from_row(row, host, install_pricing)

    def machine(self, target_id: str) -> HostMachine:
        """What a host target's machine is, as its launcher reads it (GET /machine)."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        if not isinstance(spec, HostTargetSpec):
            raise TargetInputError(["only a host target has a launcher to read its machine"])
        return self._require_host_deployer().machine(spec)

    # --- a host target's credentials (write-only, like saved targets' keys)

    def set_credential(self, target_id: str, slot: str, raw: bytes) -> ManagedTargetRow:
        """Keep a pasted token in a Secret servtuner owns, or point at a Secret the user names."""
        row, spec = self._host_row(target_id, slot)
        body = parse_body(raw, CredentialInput)
        if body.username is not None or body.password is not None:
            raise TargetInputError(["a host credential is a token: give value or external"])
        if (body.value is None) == (body.external is None):
            raise TargetInputError(["give exactly one of value and external"])
        if body.external is not None:
            ref = SecretKeyRef(name=body.external.name, key=body.external.key or TOKEN_KEY)
        else:
            if self._secret_store is None:
                raise SecretStoreUnavailableError(
                    "no secret store is configured (SERVTUNER_SECRET_STORE), so servtuner cannot "
                    "keep the token; name a Secret you manage with external instead"
                )
            assert body.value is not None
            name = managed_credential_name(row.id, slot)
            self._secret_store.put(name, {TOKEN_KEY: body.value.get_secret_value()}, row.id)
            ref = SecretKeyRef(name=name, key=TOKEN_KEY)
        if ref.name != managed_credential_name(row.id, slot):
            self._forget_credential(row.id, slot, spec)
        return self._save_ref(row, spec, slot, ref)

    def clear_credential(self, target_id: str, slot: str) -> ManagedTargetRow:
        row, spec = self._host_row(target_id, slot)
        self._forget_credential(row.id, slot, spec)
        return self._save_ref(row, spec, slot, None)

    def _host_row(self, target_id: str, slot: str) -> tuple[ManagedTargetRow, HostTargetSpec]:
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        if not isinstance(spec, HostTargetSpec):
            raise TargetInputError(
                ["only a host target keeps credentials here (Kubernetes: hf_token names a Secret)"]
            )
        if slot not in HOST_CREDENTIAL_FIELDS:
            raise TargetInputError(
                [f"a host target has the slots {', '.join(HOST_CREDENTIAL_FIELDS)}"]
            )
        return row, spec

    def _save_ref(
        self, row: ManagedTargetRow, spec: HostTargetSpec, slot: str, ref: SecretKeyRef | None
    ) -> ManagedTargetRow:
        changed = spec.model_copy(update={HOST_CREDENTIAL_FIELDS[slot]: ref})
        updated = self._repository.update_managed_target(
            row.id,
            spec=changed.model_dump(mode="json", exclude_none=True),
            updated_at=self._clock(),
        )
        assert updated is not None
        return updated

    def _forget_credential(
        self, target_id: str, slot: str, spec: ManagedTargetSpec | HostTargetSpec
    ) -> None:
        """Delete the Secret servtuner made for this slot, if the spec still points at it."""
        field = HOST_CREDENTIAL_FIELDS.get(slot)
        ref = getattr(spec, field, None) if field else None
        if ref is not None and ref.name == managed_credential_name(target_id, slot):
            if self._secret_store is not None:
                self._secret_store.delete(ref.name)

    def knob_args(self, target_id: str, knobs: Mapping[str, Any]) -> list[str]:
        """The flags of a knob set, from bench's knob table (for the recommendation)."""
        row = self.get(target_id)
        spec = parse_spec(row.spec)
        server = spec.server if isinstance(spec, ManagedTargetSpec) else "vllm"
        return self._require_deployer(spec).render_knobs(knobs, server)

    def _require_host_deployer(self) -> LauncherDeployer:
        if self._host_deployer is None:
            raise DeployerUnavailableError(
                "no host deployer: it needs the servtuner binary (SERVTUNER_BENCH_BINARY) "
                "and a secret store (launcher token)"
            )
        return self._host_deployer

    def _require_deployer(
        self, spec: ManagedTargetSpec | HostTargetSpec
    ) -> Deployer | LauncherDeployer:
        if isinstance(spec, HostTargetSpec):
            return self._require_host_deployer()
        if self._deployer is None:
            raise DeployerUnavailableError(
                "no deployer: managed targets are deployed by the api running in Kubernetes "
                "(the kubernetes runner) with the servtuner binary (SERVTUNER_BENCH_BINARY)"
            )
        return self._deployer

    def _checked(self, raw: bytes, current_id: str | None) -> ManagedTargetCreate:
        body = parse_body(raw, ManagedTargetCreate)
        if isinstance(body.spec, ManagedTargetSpec) and body.namespace not in self._namespaces:
            granted = ", ".join(self._namespaces) or "none"
            raise TargetInputError(
                [
                    f"namespace {body.namespace} is not a tuning namespace "
                    f"(granted: {granted}; the chart's rbac.tuning.namespaces)"
                ]
            )
        other = self._repository.managed_target_using(name=body.name, namespace=body.namespace)
        if other is not None and other.id != current_id:
            what = "name" if other.name == body.name else "namespace"
            raise TargetConflictError(
                f"managed target {other.name} already uses this {what}: one target per namespace"
            )
        host_id = getattr(body.spec, "host_id", None)
        if host_id is not None and self._repository.get_host(host_id) is None:
            raise TargetInputError([f"spec.host_id: no host {host_id}"])
        check_target_shape(run_target(body.name, body.namespace, body.spec))
        return body
