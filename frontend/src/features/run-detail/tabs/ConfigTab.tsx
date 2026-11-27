import { Badge, Code, Group, Paper, SimpleGrid, Stack, Table, Text, Title, Tooltip } from "@mantine/core";
import { parse } from "yaml";
import { useRunDocument, useSubmittedSpec } from "../../../api/hooks";
import type { AnalysisResult, Environment, RunStatus, TargetSnapshot } from "../../../api/types";
import { formatBytes } from "../../../components/format";
import { ModelName } from "../../../components/ModelName";
import { WithDocument } from "./NotAnalysed";

type KnobCheck = NonNullable<AnalysisResult["quality"]["knobs"]>[number];
type Workload = TargetSnapshot["workloads"][number];

/** The Config tab: declared vs actual knobs, transport per hop, what ran, and the target. */
export function ConfigTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const analysis = useRunDocument("getAnalysis", runId, status);
  const environment = useRunDocument("getEnvironment", runId, status);
  const snapshot = useRunDocument("getSnapshot", runId, status);
  const spec = useSubmittedSpec(runId);
  return (
    <Stack gap="xl">
      <ModelTested specText={spec.data ?? null} snapshot={snapshot.data ?? null} />
      <WithDocument query={analysis} what="knob check">
        {(document) => <Knobs checks={document.quality.knobs ?? []} />}
      </WithDocument>
      <WithDocument query={environment} what="environment">
        {(document) => <Ran environment={document} />}
      </WithDocument>
      {snapshot.data === null ? (
        <Text size="sm" c="dimmed">
          No target snapshot: the target is outside Kubernetes, or the bench Job had no cluster access. Its knobs come
          from its own endpoints instead.
        </Text>
      ) : (
        <WithDocument query={snapshot} what="target snapshot">
          {(document) => <Target snapshot={document} />}
        </WithDocument>
      )}
    </Stack>
  );
}

const KNOB_STATUS = {
  verified: {
    label: "Verified",
    color: "teal",
    help: "The target runs the declared value.",
  },
  mismatch: {
    label: "Mismatch",
    color: "red",
    help: "The target runs another value: the run is excluded from comparisons.",
  },
  unverified: {
    label: "Unverified",
    color: "gray",
    help: "The target does not expose this knob, so it could not be checked.",
  },
} as const;

function Knobs({ checks }: { checks: KnobCheck[] }) {
  return (
    <Stack gap="xs">
      <Title order={4}>Declared against actual knobs</Title>
      {checks.length === 0 ? (
        <Text size="sm" c="dimmed">
          The RunSpec declares no knobs.
        </Text>
      ) : (
        <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Knob</Table.Th>
              <Table.Th>Declared</Table.Th>
              <Table.Th>On the target</Table.Th>
              <Table.Th>Check</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {checks.map((check) => {
              const meta = KNOB_STATUS[check.status];
              return (
                <Table.Tr key={check.name}>
                  <Table.Td ff="monospace">{check.name}</Table.Td>
                  <Table.Td>{check.declared}</Table.Td>
                  <Table.Td>
                    {check.actual ?? (
                      <Text span c="dimmed">
                        not exposed
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Tooltip label={meta.help} multiline w={260}>
                      <Badge color={meta.color} variant="light" size="sm">
                        {meta.label}
                      </Badge>
                    </Tooltip>
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}

type TransportMode = NonNullable<Environment["transport"]["target"]>;

/** How each recorded mode is shown. A Record, so a new mode cannot fall through to another's badge. */
const MODE_BADGE: Record<TransportMode, { label: string; color: string; tooltip?: string }> = {
  mtls: { label: "mTLS", color: "teal" },
  tls: {
    label: "TLS",
    color: "teal",
    tooltip: "The server's certificate was verified; no client certificate was sent",
  },
  platform: {
    label: "Platform",
    color: "yellow",
    tooltip: "Plain on the wire: the network platform (CNI or mesh) is trusted to encrypt it",
  },
};

function ModeBadge({ mode }: { mode: TransportMode }) {
  const { label, color, tooltip } = MODE_BADGE[mode];
  const badge = (
    <Badge color={color} variant="light" size="sm">
      {label}
    </Badge>
  );
  return tooltip ? (
    <Tooltip label={tooltip} multiline w={260}>
      {badge}
    </Tooltip>
  ) : (
    badge
  );
}

const HOPS = [
  ["target", "Target"],
  ["prometheus", "Prometheus"],
  ["api", "API"],
  ["object_store", "Object store"],
] as const;

function Ran({ environment }: { environment: Environment }) {
  const { engine, transport } = environment;
  return (
    <SimpleGrid cols={{ base: 1, md: 2 }} spacing="lg">
      <Stack gap="xs">
        <Title order={4}>Transport per hop</Title>
        <Table verticalSpacing={4}>
          <Table.Tbody>
            {HOPS.filter(([hop]) => transport[hop] != null).map(([hop, label]) => (
              <Table.Tr key={hop}>
                <Table.Td>{label}</Table.Td>
                <Table.Td>
                  <ModeBadge mode={transport[hop]!} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
        {environment.tls_relay_used && (
          <Text size="xs" c="dimmed">
            The engine reached the target through servtuner's TLS relay, which shares the bench pod's CPU.
          </Text>
        )}
      </Stack>
      <Stack gap="xs">
        <Title order={4}>What ran</Title>
        <Table verticalSpacing={4}>
          <Table.Tbody>
            <Row label="Bench">
              {environment.bench_version} ({environment.go_version})
            </Row>
            <Row label="Engine">
              {engine.name} {engine.version}
              {engine.max_in_flight != null && (
                <Text span size="xs" c="dimmed">
                  {" "}
                  at most {engine.max_in_flight.toLocaleString("en-US")} requests in flight
                </Text>
              )}
            </Row>
            {environment.pod && (
              <Row label="Bench pod">
                {environment.namespace}/{environment.pod}
                {environment.node && ` on ${environment.node}`}
              </Row>
            )}
            {environment.workload && (
              <Row label="Workload">
                <Code>{environment.workload.hash.slice(0, 19)}</Code> {environment.workload.generator}
              </Row>
            )}
            <Row label="Clock">{environment.clock_source}</Row>
          </Table.Tbody>
        </Table>
      </Stack>
    </SimpleGrid>
  );
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Table.Tr>
      <Table.Td c="dimmed" w={120}>
        {label}
      </Table.Td>
      <Table.Td>{children}</Table.Td>
    </Table.Tr>
  );
}

function Target({ snapshot }: { snapshot: TargetSnapshot }) {
  return (
    <Stack gap="md">
      <Title order={4}>The target, as captured before the load</Title>
      {snapshot.workloads.map((workload, index) => (
        <WorkloadCard key={index} workload={workload} />
      ))}
    </Stack>
  );
}

function WorkloadCard({ workload }: { workload: Workload }) {
  const hardware = workload.hardware_profile;
  const info = workload.target_info;
  const role = workload.role === "target" ? "Target" : `Upstream ${workload.upstream_index ?? ""}`;
  return (
    <Paper withBorder p="md" radius="sm">
      <Stack gap="sm">
        <Group justify="space-between">
          <Text fw={600}>
            {role}, {workload.target_type}
          </Text>
          {workload.replicas && (
            <Text size="sm" c="dimmed">
              {workload.replicas.ready} of {workload.replicas.desired} replicas ready
            </Text>
          )}
        </Group>
        {(hardware || info) && (
          <Text size="sm">
            {hardware && hardwareText(hardware)}
            {info?.version && `, version ${info.version}`}
            {info?.models && info.models.length > 0 && `, serving ${info.models.join(", ")}`}
          </Text>
        )}
        {workload.pods.slice(0, 1).flatMap((pod) =>
          pod.containers.map((container) => (
            <Stack key={`${pod.name}/${container.name}`} gap={4}>
              <Group gap="xs">
                <Text size="sm" fw={500}>
                  {container.name}
                </Text>
                <Code>{container.image}</Code>
                {(container.restart_count ?? 0) > 0 && (
                  <Badge color="red" variant="light" size="sm">
                    {container.restart_count} restarts
                    {container.last_termination_reason ? `, ${container.last_termination_reason}` : ""}
                  </Badge>
                )}
              </Group>
              <Text size="xs" c="dimmed">
                requests {quantities(container.requests)}; limits {quantities(container.limits)}
                {workload.pods.length > 1 && ` (pod ${pod.name}, one of ${workload.pods.length})`}
              </Text>
              {container.args && container.args.length > 0 && <Code block>{container.args.join(" ")}</Code>}
              {container.env && container.env.length > 0 && (
                <Text size="xs" c="dimmed">
                  env:{" "}
                  {container.env
                    .map((variable) =>
                      variable.is_redacted ? `${variable.name} (redacted)` : `${variable.name}=${variable.value}`,
                    )
                    .join(", ")}
                </Text>
              )}
            </Stack>
          )),
        )}
      </Stack>
    </Paper>
  );
}

function hardwareText(hardware: NonNullable<Workload["hardware_profile"]>): string {
  return [
    hardware.accelerator === "none" ? "No GPU" : hardware.accelerator,
    hardware.gpu_count_per_pod != null && `${hardware.gpu_count_per_pod} GPU per pod`,
    hardware.gpu_memory_bytes != null && `${formatBytes(hardware.gpu_memory_bytes)} GPU memory`,
    `node type ${hardware.node_instance_type}`,
  ]
    .filter(Boolean)
    .join(", ");
}

function quantities(values: Record<string, string> | null | undefined): string {
  const entries = Object.entries(values ?? {});
  return entries.length === 0 ? "none" : entries.map(([name, value]) => `${name} ${value}`).join(", ");
}

interface TokenShape {
  mean?: number;
  stddev?: number;
}

/** The RunSpec fields the model card reads, as submitted (spec.yaml). */
interface SubmittedSpec {
  target?: { type?: string; endpoint?: string; model?: string };
  workload?: {
    api?: string;
    streaming?: boolean;
    dataset?: { input_tokens?: TokenShape; output_tokens?: TokenShape };
    conversation?: { turns?: number };
  };
}

function readSpec(text: string | null): SubmittedSpec | null {
  if (!text) return null;
  try {
    const document: unknown = parse(text);
    return document && typeof document === "object" ? (document as SubmittedSpec) : null;
  } catch {
    return null;
  }
}

function tokens(shape: TokenShape | undefined): string {
  if (shape?.mean == null) return "?";
  return shape.stddev ? `${shape.mean} ± ${shape.stddev}` : String(shape.mean);
}

/**
 * The model the run tested, first on the tab: results only mean something for
 * the model, server and workload they measured.
 */
function ModelTested({ specText, snapshot }: { specText: string | null; snapshot: TargetSnapshot | null }) {
  const spec = readSpec(specText);
  const model = spec?.target?.model;
  if (!model) return null;
  const served = snapshot?.workloads.find((workload) => workload.role === "target")?.target_info;
  const listed = served?.models ?? [];
  const workload = spec.workload;
  return (
    <Paper withBorder p="md" radius="sm" aria-label="Model tested" bg="var(--mantine-color-indigo-light)">
      <Stack gap={6}>
        <Group gap="sm" align="center">
          <Text fw={600}>Model tested</Text>
          <ModelName model={model} size="lg" />
          {listed.length === 0 ? (
            <Text size="sm" c="dimmed">
              the server did not report its models
            </Text>
          ) : listed.includes(model) ? (
            <Badge color="teal" variant="light">
              Listed by the server
            </Badge>
          ) : (
            <Tooltip label={`The server's /v1/models listed ${listed.join(", ")}`}>
              <Badge color="orange" variant="light">
                Not in the server's model list
              </Badge>
            </Tooltip>
          )}
        </Group>
        <Text size="sm">
          {spec.target?.type ?? "target"}
          {served?.version && ` ${served.version}`}
          {spec.target?.endpoint && (
            <>
              {" at "}
              <Code>{spec.target.endpoint}</Code>
            </>
          )}
        </Text>
        {workload && (
          <Text size="sm" c="dimmed">
            {workload.api ?? "chat"}, {workload.streaming ? "streaming" : "not streaming"}, input{" "}
            {tokens(workload.dataset?.input_tokens)} and output {tokens(workload.dataset?.output_tokens)} tokens
            {workload.conversation?.turns ? `, ${workload.conversation.turns}-turn conversations` : ""}
          </Text>
        )}
      </Stack>
    </Paper>
  );
}
