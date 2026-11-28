import {
  Accordion,
  Alert,
  Anchor,
  Button,
  Group,
  Loader,
  NumberInput,
  PasswordInput,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Textarea,
} from "@mantine/core";
import { useState } from "react";
import { type ManagedTargetSave, useHosts, useKnobCatalog, useManagedTargets, useRunDefaults } from "../../api/hooks";
import type { HostCredentialSlot, ManagedTarget } from "../../api/types";
import { FIELD_GRID_STYLE } from "../../components/layout";
import { unknownFlags } from "../knobs/baseFlags";
import { generateName } from "./names";
import {
  serverLabel,
  type ModelServer,
  buildBody,
  type CertificateSource,
  DEFAULT_GPUS,
  type FormFields,
  type FormState,
  initialState,
  HOST_METRICS_NAMESPACE,
  MAX_GPUS,
  needsCpuImage,
  pastedCredentials,
  formError,
  type Placement,
  VLLM_CPU_IMAGE,
  withRegistry,
  withLauncherUrl,
  withServer,
} from "./form";

type FieldProps = {
  value: string;
  onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => void;
};
type Field = (key: keyof FormFields) => FieldProps;

/**
 * Create or edit a managed server: on Kubernetes, in a namespace the chart granted, or on a
 * host through its servtuner launcher.
 */
type FormProps = {
  existing?: ManagedTarget;
  // A new target's placement to start in (?add=host from Tuning).
  initialPlacement?: Placement;
  submitLabel: string;
  isPending: boolean;
  error: string | undefined;
  onSubmit: (save: ManagedTargetSave) => void;
  onCancel: () => void;
};

/** An air-gapped install pulls vLLM from its own registry, so the form starts once the install's
 * defaults say which. */
export function ManagedTargetForm(props: FormProps) {
  const defaults = useRunDefaults();
  if (defaults.isPending) return <Loader size="sm" aria-label="Loading" />;
  return <FormBody {...props} registry={defaults.data?.image_registry} />;
}

function FormBody({
  registry,
  existing,
  initialPlacement,
  submitLabel,
  isPending,
  error,
  onSubmit,
  onCancel,
}: FormProps & { registry: string | null | undefined }) {
  const [state, setState] = useState<FormState>(() => initialState(existing, initialPlacement, registry));
  const update = (change: Partial<FormState>) => setState((current) => ({ ...current, ...change }));
  const field: Field = (key) => ({
    value: state.fields[key],
    onChange: (event) => {
      // Read now: React clears currentTarget before a state updater runs.
      const value = event.currentTarget.value;
      setState((current) => ({ ...current, fields: { ...current.fields, [key]: value } }));
    },
  });
  const isHost = state.placement === "host";
  const targets = useManagedTargets();
  const rename = () => {
    const taken = (targets.data ?? []).map((target) => target.name);
    setState((current) => ({ ...current, fields: { ...current.fields, name: generateName(taken) } }));
  };
  const server = isHost ? "vllm" : state.server;
  const serverName = server === "sglang" ? "SGLang" : "vLLM";
  const knobCatalog = useKnobCatalog(server);
  const unknown = knobCatalog.data ? unknownFlags(state.fields.baseFlags.split("\n"), knobCatalog.data) : [];
  const problem = formError(state);
  return (
    <Stack gap="sm" maw={900}>
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          Where it runs
        </Text>
        <SegmentedControl
          value={state.placement}
          onChange={(value) => update({ placement: value as Placement })}
          data={[
            { value: "kubernetes", label: "Kubernetes" },
            { value: "host", label: "A host with servtuner launcher" },
          ]}
        />
        <Text size="sm" c="dimmed">
          {isHost
            ? `servtuner starts and stops ${serverName} through the launcher on that host, with the args it renders. The launcher's token is a Secret in the runs namespace.`
            : `servtuner creates this ${serverName} itself, in a namespace the chart granted (rbac.tuning.namespaces), and never touches objects it did not create. Pin the image to a version.`}
        </Text>
      </Stack>
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          Model server
        </Text>
        <SegmentedControl
          value={state.server}
          onChange={(value) => setState((current) => withServer(current, value as ModelServer, registry))}
          data={[
            { value: "vllm", label: "vLLM" },
            { value: "sglang", label: "SGLang" },
          ]}
        />
        {state.server === "sglang" && (
          <Text size="sm" c="dimmed">
            {isHost
              ? "The launcher runs SGLang from the Python its sglang setting names. Its metrics are always on, and its knobs come from SGLang's own flags."
              : "The image runs python3 -m sglang.launch_server: use an SGLang image (lmsysorg/sglang), pinned to a version. Its metrics are always on, and its knobs come from SGLang's own flags."}
          </Text>
        )}
      </Stack>
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <TextInput
          label="Name"
          required
          description={
            existing ? undefined : (
              <Anchor component="button" type="button" size="xs" onClick={rename}>
                Pick another name
              </Anchor>
            )
          }
          {...field("name")}
        />
        {!isHost && (
          <TextInput
            label="Namespace"
            description="One managed target per namespace"
            required
            {...field("namespace")}
          />
        )}
        <TextInput label="Model" placeholder="Qwen/Qwen3-32B" required {...field("model")} />
        <TextInput label="Description" {...field("description")} />
        <TextInput
          label="Served model name"
          description="Optional: the name clients ask for, if not the model's"
          {...field("servedModelName")}
        />
      </SimpleGrid>
      {isHost ? (
        <HostFields
          field={field}
          state={state}
          existing={existing}
          setLauncherUrl={(launcherUrl) =>
            setState((current) => ({ ...current, fields: withLauncherUrl(current.fields, launcherUrl) }))
          }
          setHostId={(hostId) => setState((current) => ({ ...current, fields: { ...current.fields, hostId } }))}
        />
      ) : (
        <KubernetesFields field={field} state={state} update={update} registry={registry} />
      )}
      <Textarea
        label="Base flags"
        spellCheck={false} // code-like text: no misspelling underlines
        description="One per line; never tuned, e.g. --max-model-len=32768"
        autosize
        minRows={2}
        {...field("baseFlags")}
      />
      {unknown.length > 0 && (
        // A warning, not a refusal: the image may be a newer vLLM than the catalog.
        <Text size="xs" c="yellow.8" mt={-8}>
          {serverName} {knobCatalog.data?.version} has no {unknown.join(", ")}: check the spelling, unless the image
          runs a newer {serverName}.
        </Text>
      )}
      <Textarea
        label="Environment"
        spellCheck={false} // code-like text: no misspelling underlines
        description="Optional, NAME=value per line, e.g. VLLM_USE_V1=1; never a secret"
        autosize
        minRows={1}
        {...field("env")}
      />
      {problem && (
        <Text size="sm" c="red" role="alert">
          {problem}
        </Text>
      )}
      {error && (
        <Alert color="red" title="Not saved">
          {error}
        </Alert>
      )}
      <Group>
        <Button
          onClick={() => onSubmit({ body: buildBody(state, existing), credentials: pastedCredentials(state) })}
          loading={isPending}
          disabled={!!problem}
        >
          {submitLabel}
        </Button>
        <Button variant="subtle" onClick={onCancel}>
          Cancel
        </Button>
      </Group>
    </Stack>
  );
}

function KubernetesFields({
  field,
  state,
  update,
  registry,
}: {
  field: Field;
  state: FormState;
  update: (change: Partial<FormState>) => void;
  registry: string | null | undefined;
}) {
  const cpuImage = withRegistry(VLLM_CPU_IMAGE, registry);
  const serverName = serverLabel(state.server);
  return (
    <>
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <TextInput
          label="Image"
          required
          {...field("image")}
          description={
            needsCpuImage(state) ? (
              <>
                No GPU: vLLM needs its CPU build, {cpuImage}, with vllm.enforce_eager.{" "}
                <Anchor
                  component="button"
                  type="button"
                  size="xs"
                  onClick={() => update({ fields: { ...state.fields, image: cpuImage } })}
                >
                  Use it
                </Anchor>
              </>
            ) : undefined
          }
        />
        <NumberInput
          label="GPUs"
          description="0: no GPU request (a CPU-only vLLM)"
          min={0}
          max={MAX_GPUS}
          value={state.gpus}
          onChange={(value) => update({ gpus: typeof value === "number" ? value : DEFAULT_GPUS })}
        />
        <TextInput label="CPU" {...field("cpu")} />
        <TextInput label="Memory" {...field("memory")} />
        <TextInput
          label="Shared memory (/dev/shm)"
          description="Tensor parallel workers (NCCL) and large models exchange data here; it counts against Memory. Empty: no limit"
          placeholder="e.g. 16Gi"
          {...field("shmSize")}
        />
        <Select
          label="Weights"
          description={`Where ${serverName} gets the model (an install with no internet access: a mirror or a seeded claim)`}
          data={[
            { value: "hub", label: "Download from Hugging Face" },
            { value: "mirror", label: "Download from an internal mirror" },
            { value: "pvc", label: "Read from a seeded claim" },
            { value: "object_store", label: "Stream from an object store" },
          ]}
          value={state.fields.modelSource}
          allowDeselect={false}
          onChange={(value) =>
            update({
              fields: { ...state.fields, modelSource: (value ?? "hub") as FormState["fields"]["modelSource"] },
            })
          }
        />
        {(state.fields.modelSource === "hub" || state.fields.modelSource === "mirror") && (
          <TextInput label="Model cache size" description="A PVC kept across restarts" {...field("cache")} />
        )}
        {state.fields.modelSource === "mirror" && (
          <TextInput
            label="Mirror URL"
            description={`A Hugging Face mirror ${serverName} can reach (HF_ENDPOINT)`}
            placeholder="https://hf-mirror.example.internal"
            spellCheck={false}
            {...field("mirrorEndpoint")}
          />
        )}
        {state.fields.modelSource === "object_store" && (
          <>
            <TextInput
              label="Weights location"
              description="Safetensors someone put there; vLLM streams them into GPU memory (Run:ai Model Streamer)"
              placeholder="s3://models/qwen3-32b"
              spellCheck={false}
              {...field("objectUri")}
            />
            <TextInput
              label="S3-compatible endpoint"
              description="Optional: for RustFS, Ceph, MinIO and the like; empty for AWS S3, GCS or Azure"
              placeholder="https://s3.example.internal:9000"
              spellCheck={false}
              {...field("objectEndpoint")}
            />
            <TextInput
              label="Access key Secret"
              description="Optional: keys access_key_id and secret_access_key; empty uses the pod's identity"
              spellCheck={false}
              {...field("objectSecret")}
            />
          </>
        )}
        {state.fields.modelSource === "pvc" && (
          <TextInput
            label="Seeded claim"
            description="A PVC in this namespace holding the model's archive (Models); never deleted"
            spellCheck={false}
            {...field("modelClaim")}
          />
        )}
        <TextInput
          label="Hugging Face token Secret"
          description="Optional: its key token, in the same namespace"
          {...field("hfSecret")}
        />
      </SimpleGrid>
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          Transport
        </Text>
        <SegmentedControl
          value={state.transport}
          onChange={(value) => update({ transport: value as CertificateSource })}
          data={[
            { value: "plain", label: "Plain HTTP" },
            { value: "issuer", label: "TLS from a cert-manager ClusterIssuer" },
            { value: "secret", label: "TLS from a Secret" },
          ]}
        />
      </Stack>
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <Textarea
          label="Node selector"
          spellCheck={false} // code-like text: no misspelling underlines
          description="Optional, key=value per line, e.g. nvidia.com/gpu.product=NVIDIA-H200"
          autosize
          minRows={1}
          {...field("nodeSelector")}
        />
        <Textarea
          label="Tolerations"
          spellCheck={false} // code-like text: no misspelling underlines
          description="Optional, a YAML list, e.g. - {key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}"
          autosize
          minRows={1}
          {...field("tolerations")}
        />
      </SimpleGrid>
      {state.transport === "issuer" && <TextInput label="ClusterIssuer" required {...field("issuerName")} />}
      {state.transport === "secret" && (
        <TextInput
          label="TLS Secret"
          description="tls.crt, tls.key and ca.crt; used by this target only"
          required
          {...field("tlsSecret")}
        />
      )}
    </>
  );
}

function HostFields({
  field,
  state,
  existing,
  setLauncherUrl,
  setHostId,
}: {
  field: Field;
  state: FormState;
  existing: ManagedTarget | undefined;
  setLauncherUrl: (launcherUrl: string) => void;
  setHostId: (hostId: string) => void;
}) {
  const hosts = useHosts();
  const serverName = serverLabel(state.server);
  const isSet = (slot: HostCredentialSlot) => existing?.credentials?.[slot]?.is_set ?? false;
  const credentialHint = (slot: HostCredentialSlot, what: string) =>
    isSet(slot) ? `Set. Paste a new one to replace it; servtuner never shows it.` : what;
  return (
    <Stack gap="sm">
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <Select
          label="Host"
          description="The machine it runs on: its hardware and price (Hosts, below the managed servers)"
          placeholder={hosts.data?.length ? "None" : "No host saved yet"}
          clearable
          data={(hosts.data ?? []).map((host) => ({ value: host.id, label: host.name }))}
          value={state.fields.hostId || null}
          onChange={(hostId) => setHostId(hostId ?? "")}
          comboboxProps={{ withinPortal: false }}
        />
        <TextInput
          label="Launcher URL"
          description="Where servtuner launcher listens, e.g. http://gpu-host.localtest.me:9123"
          required
          value={state.fields.launcherUrl}
          onChange={(event) => setLauncherUrl(event.currentTarget.value)}
        />
        <PasswordInput
          label="Launcher token"
          description={credentialHint(
            "launcher_token",
            "The token servtuner launcher was started with (its token file). Kept in a Secret servtuner owns, write-only.",
          )}
          autoComplete="off"
          {...field("launcherToken")}
        />
        <TextInput
          label={`${serverName} endpoint`}
          description={`The base URL runs reach, ending in /v1: the launcher's host and ${serverName}'s port unless a port proxy is between`}
          required
          {...field("endpoint")}
        />
        <PasswordInput
          label={`${serverName} API key`}
          description={credentialHint(
            "api_key",
            `Optional: the key ${serverName} was given (the launcher's api key file), sent by runs.`,
          )}
          autoComplete="off"
          {...field("apiKey")}
        />
      </SimpleGrid>
      <Accordion variant="contained" chevronPosition="left">
        <Accordion.Item value="advanced">
          <Accordion.Control>Advanced: metrics labels, or Secrets you manage</Accordion.Control>
          <Accordion.Panel>
            <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
              <TextInput
                label="Metrics namespace label"
                description="The namespace label your scrape config gives its series"
                placeholder={HOST_METRICS_NAMESPACE}
                {...field("namespace")}
              />
              <TextInput
                label="Metrics job"
                description="The Prometheus scrape job for this host"
                required
                {...field("metricsJob")}
              />
              <TextInput
                label="Metrics pod label"
                description="The pod label your scrape config gives its series; the target's name if empty"
                placeholder={state.fields.name || undefined}
                {...field("metricsPod")}
              />
              <TextInput
                label="GPU exporter host label"
                description="Optional: the DCGM exporter's Hostname label"
                {...field("metricsNode")}
              />
              <TextInput
                label="Launcher token Secret"
                description="Instead of pasting: a Secret in the runs namespace, key token"
                {...field("launcherTokenSecret")}
              />
              <TextInput
                label={`${serverName} API key Secret`}
                description="Instead of pasting: a Secret in the runs namespace, key token"
                {...field("apiKeySecret")}
              />
            </SimpleGrid>
          </Accordion.Panel>
        </Accordion.Item>
      </Accordion>
    </Stack>
  );
}
