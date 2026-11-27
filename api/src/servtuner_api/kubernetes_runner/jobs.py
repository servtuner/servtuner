"""The objects of one run: a Job running ``servtuner run``, a ConfigMap with the RunSpec and a
Secret with the run token, owned by the Job."""

from lightkube.models.batch_v1 import JobSpec
from lightkube.models.core_v1 import (
    Affinity,
    Capabilities,
    ConfigMapVolumeSource,
    Container,
    EmptyDirVolumeSource,
    EnvVar,
    EnvVarSource,
    KeyToPath,
    LocalObjectReference,
    ObjectFieldSelector,
    PersistentVolumeClaimVolumeSource,
    PodAffinity,
    PodAffinityTerm,
    PodAntiAffinity,
    PodSecurityContext,
    PodSpec,
    PodTemplateSpec,
    ResourceRequirements,
    SeccompProfile,
    SecretKeySelector,
    SecretVolumeSource,
    SecurityContext,
    Volume,
    VolumeMount,
    WeightedPodAffinityTerm,
)
from lightkube.models.meta_v1 import LabelSelector, ObjectMeta, OwnerReference
from lightkube.resources.batch_v1 import Job
from lightkube.resources.core_v1 import ConfigMap, Secret

from servtuner_api.datadir import DATA_DIR_ENV, run_bundle_dir
from servtuner_api.kubernetes_runner.config import (
    BENCH_CONTAINER,
    BENCH_TLS_DIR,
    CA_DIR,
    COMPLETION_INDEX_ANNOTATION,
    COMPLETION_INDEX_ENV,
    COMPONENT_LABELS,
    HOSTNAME_TOPOLOGY,
    PRIVATE_MODE,
    RUN_ID_LABEL,
    SECRETS_DIR,
    SHARD_SPREAD_WEIGHT,
    SPEC_DIR,
    SPEC_KEY,
    TMP_DIR,
    TOKEN_DIR,
    TOKEN_KEY,
    KubernetesRunnerConfig,
    job_name,
    workloads_in,
)
from servtuner_api.runner import (
    SHARD_INDEX_FLAG,
    LaunchRequest,
    ObjectStoreClient,
    output_arguments,
    shards_in,
)
from servtuner_api.secret_store import secret_names_in
from servtuner_api.settings.env import (
    ENV_AWS_ACCESS_KEY_ID,
    ENV_AWS_SECRET_ACCESS_KEY,
    ENV_S3_CHECKSUM_MODE,
    ENV_S3_CREDENTIALS_SOURCE,
    ENV_S3_ENDPOINT,
    ENV_S3_FORCE_PATH_STYLE,
    ENV_S3_PART_SIZE_MIB,
    ENV_S3_REGION,
    NULL_ENGINE,
)
from servtuner_api.settings.models import AntiAffinity, TransportMode


class JobBuilder:
    """Builds the objects of one run. Pure: no cluster access, so it is unit-tested whole."""

    def __init__(self, config: KubernetesRunnerConfig) -> None:
        self._config = config

    def command(self, run_id: str, shards: int = 1) -> list[str]:
        """The same contract as SubprocessRunner.build_command, with in-pod paths."""
        config = self._config
        args = [
            "run",
            "--spec",
            str(SPEC_DIR / SPEC_KEY),
            *output_arguments(
                str(run_bundle_dir(config.data_dir, run_id)),
                config.s3,
                ObjectStoreClient(
                    config.object_store_mode,
                    str(BENCH_TLS_DIR / "tls.crt"),
                    str(BENCH_TLS_DIR / "tls.key"),
                    str(CA_DIR / "ca.crt"),
                ),
            ),
            "--run-id",
            run_id,
            "--api-url",
            config.api_url,
            "--run-token-file",
            str(TOKEN_DIR / TOKEN_KEY),
            "--api-tls-mode",
            config.api_tls_mode.value,
        ]
        if config.engine_override == NULL_ENGINE:
            args += ["--engine-override", NULL_ENGINE]
        if config.offline:
            args.append("--offline")
        if shards > 1:
            # Kubernetes expands $(VAR) from the container's env (_env).
            args += [SHARD_INDEX_FLAG, f"$({COMPLETION_INDEX_ENV})"]
        if config.api_tls_mode is TransportMode.MTLS:
            args += [
                "--tls-cert-file",
                str(BENCH_TLS_DIR / "tls.crt"),
                "--tls-key-file",
                str(BENCH_TLS_DIR / "tls.key"),
                "--tls-ca-file",
                str(CA_DIR / "ca.crt"),
            ]
        return args

    def labels(self, run_id: str) -> dict[str, str]:
        return {**COMPONENT_LABELS, RUN_ID_LABEL: run_id.lower()}

    def spec_config_map(self, request: LaunchRequest, owner: OwnerReference) -> ConfigMap:
        return ConfigMap(
            apiVersion="v1",
            kind="ConfigMap",
            metadata=self._metadata(request.run_id, owner),
            data={SPEC_KEY: request.spec_raw.decode("utf-8")},
        )

    def token_secret(self, request: LaunchRequest, owner: OwnerReference) -> Secret:
        return Secret(
            apiVersion="v1",
            kind="Secret",
            metadata=self._metadata(request.run_id, owner),
            type="Opaque",
            stringData={TOKEN_KEY: request.run_token},
        )

    def job(self, run_id: str, spec: object) -> Job:
        cluster = self._config.cluster
        name = job_name(run_id)
        shards = shards_in(spec)
        job_spec = JobSpec(
            backoffLimit=0,
            ttlSecondsAfterFinished=cluster.job_ttl_seconds,
            template=PodTemplateSpec(
                metadata=ObjectMeta(labels=self.labels(run_id)),
                spec=self._pod_spec(run_id, spec, shards),
            ),
        )
        if shards > 1:
            # One pod per shard, all at once: the leader (index 0) waits for the
            # others to be ready before any load starts.
            job_spec.completionMode = "Indexed"
            job_spec.completions = shards
            job_spec.parallelism = shards
        return Job(
            apiVersion="batch/v1",
            kind="Job",
            metadata=ObjectMeta(name=name, namespace=cluster.namespace, labels=self.labels(run_id)),
            spec=job_spec,
        )

    def _metadata(self, run_id: str, owner: OwnerReference) -> ObjectMeta:
        return ObjectMeta(
            name=job_name(run_id),
            namespace=self._config.cluster.namespace,
            labels=self.labels(run_id),
            ownerReferences=[owner],
        )

    def _pod_spec(self, run_id: str, spec: object, shards: int = 1) -> PodSpec:
        cluster = self._config.cluster
        resources = {"cpu": cluster.bench_cpu, "memory": cluster.bench_memory}
        volumes, mounts = self._volumes(run_id, spec)
        return PodSpec(
            serviceAccountName=cluster.service_account,
            restartPolicy="Never",
            imagePullSecrets=[
                LocalObjectReference(name=name) for name in cluster.image_pull_secrets
            ]
            or None,
            securityContext=PodSecurityContext(
                runAsNonRoot=True,
                fsGroup=cluster.data_fs_group,
                seccompProfile=SeccompProfile(type="RuntimeDefault"),
            ),
            affinity=self._affinity(spec, run_id, shards),
            containers=[
                Container(
                    name=BENCH_CONTAINER,
                    image=cluster.bench_image,
                    args=self.command(run_id, shards),
                    env=self._env(shards),
                    # Requests equal limits: Guaranteed QoS, so the load generator is
                    # neither throttled below its request nor evicted first.
                    resources=ResourceRequirements(requests=resources, limits=resources),
                    securityContext=SecurityContext(
                        allowPrivilegeEscalation=False,
                        readOnlyRootFilesystem=True,
                        capabilities=Capabilities(drop=["ALL"]),
                    ),
                    volumeMounts=mounts,
                )
            ],
            volumes=volumes,
        )

    def _env(self, shards: int = 1) -> list[EnvVar]:
        def field(path: str) -> EnvVarSource:
            return EnvVarSource(fieldRef=ObjectFieldSelector(fieldPath=path))

        # bench records where it ran (environment.json), and analysis finds the
        # bench pod's own CPU series by POD_NAME (client_bound).
        return [
            EnvVar(name="POD_NAME", valueFrom=field("metadata.name")),
            EnvVar(name="POD_NAMESPACE", valueFrom=field("metadata.namespace")),
            EnvVar(name="NODE_NAME", valueFrom=field("spec.nodeName")),
            EnvVar(name=DATA_DIR_ENV, value=str(self._config.data_dir)),
            *self._s3_env(),
            *(
                [
                    EnvVar(
                        name=COMPLETION_INDEX_ENV,
                        valueFrom=field(f"metadata.annotations['{COMPLETION_INDEX_ANNOTATION}']"),
                    )
                ]
                if shards > 1
                else []
            ),
        ]

    def _s3_env(self) -> list[EnvVar]:
        """The object store's settings, and its keys by reference to their Secret."""
        s3 = self._config.s3
        if s3 is None:
            return []
        values = {
            ENV_S3_REGION: s3.region,
            ENV_S3_FORCE_PATH_STYLE: "true" if s3.force_path_style else "false",
            ENV_S3_CHECKSUM_MODE: s3.checksum_mode.value,
            ENV_S3_CREDENTIALS_SOURCE: s3.credentials_source.value,
            ENV_S3_PART_SIZE_MIB: str(s3.part_size_mib),
        }
        if s3.endpoint is not None:
            values[ENV_S3_ENDPOINT] = s3.endpoint
        env = [EnvVar(name=name, value=value) for name, value in values.items()]
        if self._config.cluster.s3_credentials is not None:
            secret, access_key_key, secret_key_key = self._config.cluster.s3_credentials
            for name, key in (
                (ENV_AWS_ACCESS_KEY_ID, access_key_key),
                (ENV_AWS_SECRET_ACCESS_KEY, secret_key_key),
            ):
                env.append(
                    EnvVar(
                        name=name,
                        valueFrom=EnvVarSource(
                            secretKeyRef=SecretKeySelector(name=secret, key=key)
                        ),
                    )
                )
        return env

    def _volumes(self, run_id: str, spec: object) -> tuple[list[Volume], list[VolumeMount]]:
        cluster = self._config.cluster
        name = job_name(run_id)
        volumes = [
            Volume(name="spec", configMap=ConfigMapVolumeSource(name=name)),
            Volume(
                name="run-token",
                secret=SecretVolumeSource(secretName=name, defaultMode=PRIVATE_MODE),
            ),
            Volume(name="tmp", emptyDir=EmptyDirVolumeSource()),
        ]
        mounts = [
            VolumeMount(name="spec", mountPath=str(SPEC_DIR), readOnly=True),
            VolumeMount(name="run-token", mountPath=str(TOKEN_DIR), readOnly=True),
            VolumeMount(name="tmp", mountPath=str(TMP_DIR)),
        ]
        if cluster.data_claim is not None:
            volumes.append(
                Volume(
                    name="data",
                    persistentVolumeClaim=PersistentVolumeClaimVolumeSource(
                        claimName=cluster.data_claim
                    ),
                )
            )
            mounts.append(VolumeMount(name="data", mountPath=str(self._config.data_dir)))
        if cluster.bench_tls_secret is not None:
            volumes.append(
                Volume(
                    name="tls-bench",
                    secret=SecretVolumeSource(
                        secretName=cluster.bench_tls_secret, defaultMode=PRIVATE_MODE
                    ),
                )
            )
            mounts.append(
                VolumeMount(name="tls-bench", mountPath=str(BENCH_TLS_DIR), readOnly=True)
            )
        if cluster.ca_secret is not None:
            volumes.append(
                Volume(
                    name="tls-ca",
                    secret=SecretVolumeSource(
                        secretName=cluster.ca_secret, items=[KeyToPath(key="ca.crt", path="ca.crt")]
                    ),
                )
            )
            mounts.append(VolumeMount(name="tls-ca", mountPath=str(CA_DIR), readOnly=True))
        for index, secret_name in enumerate(secret_names_in(spec)):
            volume_name = f"secret-{index}"
            volumes.append(
                Volume(
                    name=volume_name,
                    secret=SecretVolumeSource(secretName=secret_name, defaultMode=PRIVATE_MODE),
                )
            )
            mounts.append(
                VolumeMount(
                    name=volume_name, mountPath=str(SECRETS_DIR / secret_name), readOnly=True
                )
            )
        return volumes, mounts

    def _affinity(self, spec: object, run_id: str, shards: int = 1) -> Affinity | None:
        cluster = self._config.cluster
        terms = [
            PodAffinityTerm(
                labelSelector=LabelSelector(matchLabels=workload.selector),
                namespaces=[workload.namespace],
                topologyKey=HOSTNAME_TOPOLOGY,
            )
            for workload in workloads_in(spec)
        ]
        anti_affinity: PodAntiAffinity | None = None
        if terms and cluster.target_anti_affinity is AntiAffinity.REQUIRED:
            anti_affinity = PodAntiAffinity(requiredDuringSchedulingIgnoredDuringExecution=terms)
        elif terms:
            anti_affinity = PodAntiAffinity(
                preferredDuringSchedulingIgnoredDuringExecution=[
                    WeightedPodAffinityTerm(weight=100, podAffinityTerm=term) for term in terms
                ]
            )
        if shards > 1:
            # Shards add client capacity only on different nodes; preferred, so a
            # one-node cluster (or a ReadWriteOnce data volume) still schedules them.
            spread = WeightedPodAffinityTerm(
                weight=SHARD_SPREAD_WEIGHT,
                podAffinityTerm=PodAffinityTerm(
                    labelSelector=LabelSelector(matchLabels={RUN_ID_LABEL: run_id.lower()}),
                    topologyKey=HOSTNAME_TOPOLOGY,
                ),
            )
            anti_affinity = anti_affinity or PodAntiAffinity()
            anti_affinity.preferredDuringSchedulingIgnoredDuringExecution = [
                *(anti_affinity.preferredDuringSchedulingIgnoredDuringExecution or []),
                spread,
            ]
        affinity: PodAffinity | None = None
        if cluster.node_pinned and cluster.api_component_label is not None:
            key, value = cluster.api_component_label
            # A ReadWriteOnce volume attaches to one node: the api's.
            affinity = PodAffinity(
                requiredDuringSchedulingIgnoredDuringExecution=[
                    PodAffinityTerm(
                        labelSelector=LabelSelector(matchLabels={key: value}),
                        namespaces=[cluster.api_namespace],
                        topologyKey=HOSTNAME_TOPOLOGY,
                    )
                ]
            )
        if anti_affinity is None and affinity is None:
            return None
        return Affinity(podAntiAffinity=anti_affinity, podAffinity=affinity)
