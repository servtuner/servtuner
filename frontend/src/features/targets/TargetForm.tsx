import {
  Accordion,
  Button,
  Code,
  Group,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Textarea,
  Title,
} from "@mantine/core";
import { useState } from "react";
import type { SavedTarget, TargetCreate } from "../../api/types";
import { FIELD_GRID_STYLE } from "../../components/layout";
import { tlsBlock, type TlsMode } from "../../api/tls";
import { PricingSection } from "./PricingSection";
import { type PricingFields, pricingBody, pricingError, pricingFields } from "./pricing";

type Location = "workload" | "metrics" | "none";

const TYPE_HELP: Record<string, string> = {
  vllm: "A vLLM server. Its /metrics feed the KV cache, queue and token panels.",
  litellm: "A LiteLLM proxy. The API key is its master key or a virtual key.",
  bifrost: "A Bifrost gateway. Add an admin credential if /api/config sits behind auth.",
  "openai-compatible": "Any OpenAI-compatible server. Only client-side metrics are measured.",
};

/** The form's own fields; anything else in an existing target (upstreams, …) is kept as it is. */
interface FormState {
  name: string;
  description: string;
  type: string;
  endpoint: string;
  model: string;
  tlsMode: TlsMode;
  /** mtls: the client certificate Secret; tls: the optional private CA Secret. */
  tlsSecret: string;
  location: Location;
  namespace: string;
  selector: string;
  pod: string;
  node: string;
  job: string;
}

/** The scrape job a target outside Kubernetes is found under by default (RunSpec target.metrics.job). */
const DEFAULT_JOB = "servtuner-external";

function initialState(existing: SavedTarget | undefined): FormState {
  const target = (existing?.target ?? {}) as Record<string, unknown>;
  const tls = (target.tls ?? {}) as { mode?: string; secretRef?: { name?: string }; caSecretRef?: { name?: string } };
  const workload = target.workload as { namespace?: string; selector?: Record<string, string> } | undefined;
  const metrics = target.metrics as { namespace?: string; pod?: string; node?: string; job?: string } | undefined;
  return {
    name: existing?.name ?? "",
    description: existing?.description ?? "",
    type: (target.type as string | undefined) ?? "vllm",
    endpoint: (target.endpoint as string | undefined) ?? "",
    model: (target.model as string | undefined) ?? "",
    tlsMode: tls.mode === "mtls" || tls.mode === "tls" ? tls.mode : "platform",
    tlsSecret: tls.secretRef?.name ?? tls.caSecretRef?.name ?? "",
    location: workload ? "workload" : metrics ? "metrics" : "none",
    namespace: workload?.namespace ?? metrics?.namespace ?? "",
    selector: Object.entries(workload?.selector ?? {})
      .map(([key, value]) => `${key}=${value}`)
      .join(", "),
    pod: metrics?.pod ?? "",
    node: metrics?.node ?? "",
    job: metrics?.job ?? DEFAULT_JOB,
  };
}

function parseSelector(text: string): Record<string, string> {
  const selector: Record<string, string> = {};
  for (const part of text.split(",")) {
    const [key, ...rest] = part.split("=");
    if (key?.trim() && rest.length > 0) selector[key.trim()] = rest.join("=").trim();
  }
  return selector;
}

function toBody(state: FormState, existing: SavedTarget | undefined, pricing: PricingFields): TargetCreate {
  // The form owns these; keys are never in this body (they are set on the target's page).
  const owned = new Set(["auth", "admin", "workload", "metrics"]);
  const kept = Object.fromEntries(
    Object.entries((existing?.target ?? {}) as Record<string, unknown>).filter(([key]) => !owned.has(key)),
  );
  const target: Record<string, unknown> = {
    ...kept,
    type: state.type,
    endpoint: state.endpoint.trim(),
    model: state.model.trim(),
    tls: tlsBlock(state.tlsMode, state.tlsSecret.trim()),
  };
  if (state.location === "workload") {
    target.workload = { namespace: state.namespace.trim(), selector: parseSelector(state.selector) };
  } else if (state.location === "metrics") {
    target.metrics = {
      namespace: state.namespace.trim(),
      pod: state.pod.trim(),
      ...(state.job.trim() && state.job.trim() !== DEFAULT_JOB ? { job: state.job.trim() } : {}),
      ...(state.node.trim() ? { node: state.node.trim() } : {}),
    };
  }
  return {
    name: state.name.trim(),
    ...(state.description.trim() ? { description: state.description.trim() } : {}),
    target,
    pricing: pricingBody(pricing),
  };
}

/** Create or edit a saved target. It never holds a key: keys are set on the target's page. */
export function TargetForm({
  existing,
  submitLabel,
  isPending,
  error,
  onSubmit,
  onCancel,
}: {
  existing?: SavedTarget;
  submitLabel: string;
  isPending: boolean;
  error?: string;
  onSubmit: (body: TargetCreate) => void;
  onCancel?: () => void;
}) {
  const [state, setState] = useState(() => initialState(existing));
  const [pricing, setPricing] = useState(() => pricingFields(existing?.pricing));
  const pricingProblem = pricingError(pricing);
  const set =
    <K extends keyof FormState>(key: K) =>
    (value: FormState[K]) =>
      setState((current) => ({ ...current, [key]: value }));
  const field = (key: keyof FormState) => ({
    value: state[key] as string,
    onChange: (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) =>
      set(key)(event.currentTarget.value as never),
  });

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (pricingProblem === null) onSubmit(toBody(state, existing, pricing));
      }}
    >
      <Stack gap="md">
        <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
          <TextInput label="Name" description="Lower case, digits and dashes" required {...field("name")} />
          <Select
            label="Type"
            data={["vllm", "litellm", "bifrost", "openai-compatible"]}
            value={state.type}
            onChange={(value) => value && set("type")(value)}
            description={TYPE_HELP[state.type]}
            allowDeselect={false}
          />
        </SimpleGrid>
        <Textarea label="Description" autosize minRows={1} {...field("description")} />
        <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
          <TextInput label="Endpoint" placeholder="http://vllm.llm.svc:8000/v1" required {...field("endpoint")} />
          <TextInput label="Model" description="As the server lists it in /v1/models" required {...field("model")} />
        </SimpleGrid>
        <Group align="end">
          <Stack gap={4}>
            <Text size="sm" fw={500}>
              Transport
            </Text>
            <SegmentedControl
              value={state.tlsMode}
              onChange={(value) => set("tlsMode")(value as FormState["tlsMode"])}
              data={[
                { value: "platform", label: "Platform (plain on the wire)" },
                { value: "tls", label: "TLS" },
                { value: "mtls", label: "mTLS" },
              ]}
            />
          </Stack>
          {state.tlsMode === "mtls" && (
            <TextInput
              label="Client certificate Secret"
              description="A kubernetes.io/tls Secret, by name only"
              required
              {...field("tlsSecret")}
            />
          )}
          {state.tlsMode === "tls" && (
            <TextInput
              label="CA Secret"
              description="Optional: a Secret whose ca.crt signed the server. No client certificate is sent."
              {...field("tlsSecret")}
            />
          )}
        </Group>
        <Title order={5}>Where Prometheus finds its metrics</Title>
        <SegmentedControl
          value={state.location}
          onChange={(value) => set("location")(value as Location)}
          data={[
            { value: "workload", label: "Kubernetes workload" },
            { value: "metrics", label: "Outside Kubernetes" },
            { value: "none", label: "Not collected" },
          ]}
        />
        <LocationHelp state={state} />
        {state.location === "workload" && (
          <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
            <TextInput label="Namespace" description="Where the target's pods run" required {...field("namespace")} />
            <TextInput
              label="Pod selector"
              placeholder="app=vllm"
              description="key=value, comma-separated"
              required
              {...field("selector")}
            />
          </SimpleGrid>
        )}
        {state.location === "metrics" && (
          <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
            <TextInput
              label="Scrape job"
              description="The Prometheus job that scrapes this server"
              required
              {...field("job")}
            />
            <TextInput
              label="namespace label"
              description="The value the job attaches as namespace"
              required
              {...field("namespace")}
            />
            <TextInput label="pod label" description="The value the job attaches as pod" required {...field("pod")} />
            <TextInput
              label="GPU hostname label"
              description="Optional: the hostname its DCGM exporter's job sets"
              {...field("node")}
            />
          </SimpleGrid>
        )}
        {state.location !== "none" && <SelectorPreview state={state} />}
        <PricingSection
          fields={pricing}
          onChange={(changes) => setPricing((current) => ({ ...current, ...changes }))}
        />
        {error && (
          <Text size="sm" c="red" role="alert">
            {error}
          </Text>
        )}
        <Group>
          <Button
            type="submit"
            loading={isPending}
            disabled={pricingProblem !== null}
            title={pricingProblem ?? undefined}
          >
            {submitLabel}
          </Button>
          {onCancel && (
            <Button variant="default" onClick={onCancel}>
              Cancel
            </Button>
          )}
          {pricingProblem && (
            <Text size="xs" c="dimmed">
              {pricingProblem}
            </Text>
          )}
        </Group>
      </Stack>
    </form>
  );
}

/** What each choice means, in plain words (RunSpec target.metrics). */
function LocationHelp({ state }: { state: FormState }) {
  if (state.location === "workload") {
    return (
      <Text size="sm" c="dimmed">
        The target runs as pods in this cluster. servtuner finds them with the Kubernetes API, records their config and
        resources, and reads their series from Prometheus by namespace and pod name. Prometheus must already scrape them
        (a ServiceMonitor or PodMonitor, or the pod label <Code>servtuner.dev/scrape=true</Code>).
      </Text>
    );
  }
  if (state.location === "none") {
    return (
      <Text size="sm" c="dimmed">
        No server-side metrics: runs measure what the client sees (TTFT, latency, RPS), with no server or GPU panels.
      </Text>
    );
  }
  return (
    <Stack gap="xs">
      <Text size="sm" c="dimmed">
        For a server that is not a pod: a VM, a bare-metal box, a GPU host on the LAN. servtuner does not set up
        scraping. Prometheus must already scrape the server with a job of its own, and because there is no real pod to
        take names from, that job attaches a <Code>namespace</Code> and a <Code>pod</Code> label of its choosing to
        every series. Enter the same values here: servtuner uses them only to find this server's series among everything
        Prometheus holds, exactly as it would find a pod's.
      </Text>
      <Text size="sm" c="dimmed">
        For GPU panels, the job that scrapes the host's DCGM exporter sets a <Code>hostname</Code> label; enter it as
        the GPU hostname label. Check connection confirms both are found.
      </Text>
      <Accordion variant="contained" chevronPosition="left">
        <Accordion.Item value="example">
          <Accordion.Control>
            <Text size="sm">Example scrape job (Prometheus config)</Text>
          </Accordion.Control>
          <Accordion.Panel>
            <Code block>{exampleScrapeJob(state)}</Code>
            <Text size="xs" c="dimmed" mt="xs">
              The full example, with the DCGM job, is in deploy/prometheus/servtuner-external.values.yaml.
            </Text>
          </Accordion.Panel>
        </Accordion.Item>
      </Accordion>
    </Stack>
  );
}

function exampleScrapeJob(state: FormState): string {
  const host = (() => {
    try {
      return new URL(state.endpoint).host || "HOST:PORT";
    } catch {
      return "HOST:PORT";
    }
  })();
  return [
    `- job_name: ${state.job || DEFAULT_JOB}`,
    "  scrape_interval: 5s",
    "  static_configs:",
    `    - targets: ["${host}"]`,
    "      labels:",
    `        namespace: ${state.namespace || "<namespace label>"}`,
    `        pod: ${state.pod || "<pod label>"}`,
  ].join("\n");
}

/** The selectors servtuner's queries will use, built from the fields as they are typed. */
function SelectorPreview({ state }: { state: FormState }) {
  const lines =
    state.location === "workload"
      ? [`{namespace="${state.namespace || "…"}", pod=~"<pods matching ${state.selector || "…"}>"}`]
      : [
          `{job="${state.job || DEFAULT_JOB}", namespace="${state.namespace || "…"}", pod="${state.pod || "…"}"}`,
          ...(state.node ? [`DCGM_…{hostname="${state.node}"}`] : []),
        ];
  return (
    <Stack gap={4}>
      <Text size="xs" c="dimmed">
        servtuner will look for series matching
      </Text>
      <Code block aria-label="Prometheus selector">
        {lines.join("\n")}
      </Code>
    </Stack>
  );
}
