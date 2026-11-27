import {
  ActionIcon,
  Alert,
  Anchor,
  Button,
  Group,
  List,
  Loader,
  NumberInput,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Table,
  Tabs,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import { useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { isMap, isSeq, parseDocument, type Document } from "yaml";
import {
  useCreateRun,
  useManagedTarget,
  useRunDefaults,
  useRuns,
  useSubmittedSpec,
  useValidateSpec,
} from "../../api/hooks";
import { runLabelFilter } from "../managed-targets/summary";
import { STARTER_SPEC, starterForManagedTarget, withRunDefaults } from "./starter";
import { tlsBlock, type TlsMode } from "../../api/tls";
import { TARGET_LABEL, TargetPicker, type Change } from "./TargetPicker";
import { WorkloadPicker } from "./WorkloadPicker";
import { FIELD_GRID_STYLE } from "../../components/layout";
import { QueryError } from "../../components/QueryError";
import { ExperimentInput } from "../../components/ExperimentInput";

type Path = (string | number)[];

/**
 * New run. The form and the YAML tab edit one document: the form
 * writes into the parsed YAML (keeping its comments), so switching tabs never
 * loses a field. Validate is a dry run of servtuner validate, the authoritative
 * rules; Create run submits the document as JSON.
 */
export function NewRunPage() {
  const [searchParams] = useSearchParams();
  const managedId = searchParams.get("managed");
  if (managedId) return <ManagedTargetRun managedId={managedId} />;
  return <CloneOrNewRun from={searchParams.get("from")} />;
}

function CloneOrNewRun({ from }: { from: string | null }) {
  const clone = useSubmittedSpec(from);
  const defaults = useRunDefaults();
  if (from && clone.isPending) return <Loader size="sm" aria-label="Loading the run to clone" />;
  if (!from && defaults.isPending) return <Loader size="sm" aria-label="Loading the run defaults" />;
  // The editor mounts once its starting text is known, so its state starts there.
  return (
    <SpecEditor
      key={from ?? "new"}
      from={from}
      // A clone keeps its own spec; the template gets the install's defaults (a failed read: none).
      initialText={(from && clone.data) || withRunDefaults(STARTER_SPEC, defaults.data)}
      missingClone={!!from && clone.data === null}
    />
  );
}

/** ?managed=<id>: benchmark a managed vLLM (its page's Benchmark button), around its last SLO-max. */
function ManagedTargetRun({ managedId }: { managedId: string }) {
  const target = useManagedTarget(managedId);
  const runs = useRuns({ label: target.data ? runLabelFilter(target.data) : "" }, Boolean(target.data));
  const defaults = useRunDefaults();
  if (target.isPending || defaults.isPending || (target.isSuccess && runs.isPending))
    return <Loader size="sm" aria-label="Loading the managed target" />;
  if (target.isError) return <QueryError title="Could not load the managed target" error={target.error} />;
  // Newest first: the latest measurement of this target. A failed runs query only loses the hint.
  const sloMaxRps = runs.data?.find((run) => run.slo_max_rps != null)?.slo_max_rps ?? undefined;
  return (
    <SpecEditor
      key={managedId}
      from={null}
      // Its prices come with it (run_analysis: its host's, or the price book's), never the book's twice.
      initialText={withRunDefaults(starterForManagedTarget(target.data, sloMaxRps), {
        ...defaults.data,
        infra_pricing: undefined,
      })}
      missingClone={false}
    />
  );
}

function SpecEditor({
  from,
  initialText,
  missingClone,
}: {
  from: string | null;
  initialText: string;
  missingClone: boolean;
}) {
  const [text, setText] = useState(initialText);
  const document = useMemo(() => parseDocument(text), [text]);
  const parseErrors = document.errors.map((error) => error.message);
  const editAll = (changes: Change[]) => {
    const next = parseDocument(text);
    if (next.errors.length > 0) return;
    for (const [path, value] of changes) {
      if (value === undefined || value === "") next.deleteIn(path);
      else next.setIn(path, value);
    }
    setText(next.toString());
  };
  const edit = (path: Path, value: unknown) => editAll([[path, value]]);

  return (
    <Stack gap="md">
      <Anchor component={Link} to="/runs" size="sm">
        All runs
      </Anchor>
      <Title order={2}>New run</Title>
      {missingClone && (
        <Alert color="gray" title="Nothing to clone">
          Run {from} has no submitted RunSpec in its bundle yet; starting from the template.
        </Alert>
      )}
      <Tabs defaultValue="form" keepMounted={false}>
        <Tabs.List mb="md">
          <Tabs.Tab value="form">Form</Tabs.Tab>
          <Tabs.Tab value="yaml">YAML</Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="form">
          {parseErrors.length > 0 ? (
            <Alert color="orange" title="The YAML does not parse">
              Fix it on the YAML tab first: {parseErrors[0]}
            </Alert>
          ) : (
            <SpecForm document={document} edit={edit} editAll={editAll} />
          )}
        </Tabs.Panel>
        <Tabs.Panel value="yaml">
          <Textarea
            aria-label="RunSpec YAML"
            value={text}
            onChange={(event) => setText(event.currentTarget.value)}
            autosize
            minRows={24}
            spellCheck={false}
            styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)", fontSize: 13 } }}
          />
          {parseErrors.map((message) => (
            <Text key={message} size="sm" c="red" role="alert">
              {message}
            </Text>
          ))}
        </Tabs.Panel>
      </Tabs>
      <Actions text={text} document={document} />
    </Stack>
  );
}

function get(document: Document, path: Path): unknown {
  const value = document.getIn(path);
  return isMap(value) || isSeq(value) ? value.toJSON() : value;
}

function SpecForm({
  document,
  edit,
  editAll,
}: {
  document: Document;
  edit: (path: Path, value: unknown) => void;
  editAll: (changes: Change[]) => void;
}) {
  const text = (path: Path) => {
    const value = get(document, path);
    return value === undefined || value === null ? "" : String(value);
  };
  const tlsMode = text(["target", "tls", "mode"]) || "mtls";
  return (
    <Stack gap="lg">
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <TextInput label="Name" value={text(["name"])} onChange={(e) => edit(["name"], e.currentTarget.value)} />
        <ExperimentInput
          description="Runs with the same experiment are compared together"
          value={text(["experiment"])}
          onChange={(value) => edit(["experiment"], value)}
        />
      </SimpleGrid>
      <Title order={4}>Target</Title>
      <TargetPicker
        selected={text(["labels", TARGET_LABEL]) || undefined}
        target={get(document, ["target"])}
        prometheus={get(document, ["prometheus"])}
        edit={editAll}
      />
      <SimpleGrid cols={{ base: 1, md: 3 }} style={FIELD_GRID_STYLE}>
        <Select
          label="Type"
          data={["vllm", "bifrost", "litellm", "openai-compatible"]}
          value={text(["target", "type"])}
          onChange={(value) => value && edit(["target", "type"], value)}
        />
        <TextInput
          label="Endpoint"
          value={text(["target", "endpoint"])}
          onChange={(e) => edit(["target", "endpoint"], e.currentTarget.value)}
        />
        <TextInput
          label="Model"
          value={text(["target", "model"])}
          onChange={(e) => edit(["target", "model"], e.currentTarget.value)}
        />
      </SimpleGrid>
      <Group align="end">
        <Stack gap={4}>
          <Text size="sm" fw={500}>
            Transport
          </Text>
          <SegmentedControl
            value={tlsMode}
            onChange={(value) =>
              edit(["target", "tls"], tlsBlock(value as TlsMode, value === "mtls" ? "servtuner-client" : ""))
            }
            data={[
              { value: "mtls", label: "mTLS" },
              { value: "tls", label: "TLS" },
              { value: "platform", label: "Platform (plain on the wire)" },
            ]}
          />
        </Stack>
        {tlsMode === "mtls" && (
          <TextInput
            label="Client certificate Secret"
            description="A kubernetes.io/tls Secret, by name only"
            value={text(["target", "tls", "secretRef", "name"])}
            onChange={(e) => edit(["target", "tls", "secretRef", "name"], e.currentTarget.value)}
          />
        )}
        {tlsMode === "tls" && (
          <TextInput
            label="CA Secret"
            description="Optional: a Secret whose ca.crt signed the server. No client certificate is sent."
            value={text(["target", "tls", "caSecretRef", "name"])}
            onChange={(e) => edit(["target", "tls"], tlsBlock("tls", e.currentTarget.value.trim()))}
          />
        )}
      </Group>
      <Title order={4}>Workload</Title>
      <WorkloadPicker
        workload={get(document, ["workload"]) as { preset?: string; streaming?: boolean } | undefined}
        engine={text(["engine", "type"])}
        edit={editAll}
      />
      <Title order={4}>Load</Title>
      <Group align="end">
        <Select
          label="Engine"
          data={["loadgen", "inference-perf"]}
          value={text(["engine", "type"])}
          onChange={(value) => value && edit(["engine", "type"], value)}
          w={200}
        />
        <Select
          label="Arrivals"
          data={["constant", "poisson", "linear", "sine"]}
          value={text(["load", "pacing"]) || "constant"}
          onChange={(value) => value && edit(["load", "pacing"], value)}
          w={200}
        />
        <TextInput
          label="Warmup"
          value={text(["load", "warmup"])}
          onChange={(e) => edit(["load", "warmup"], e.currentTarget.value)}
          w={140}
        />
      </Group>
      <Stages
        stages={(get(document, ["load", "stages"]) as { rate: number; duration: string }[] | undefined) ?? []}
        edit={edit}
      />
      <Title order={4}>SLOs</Title>
      <Group>
        <TextInput
          label="TTFT p95"
          placeholder="500ms"
          value={text(["slos", "ttft_p95"])}
          onChange={(e) => edit(["slos", "ttft_p95"], e.currentTarget.value)}
          w={160}
        />
        <TextInput
          label="ITL p95"
          placeholder="50ms"
          value={text(["slos", "itl_p95"])}
          onChange={(e) => edit(["slos", "itl_p95"], e.currentTarget.value)}
          w={160}
        />
        <TextInput
          label="E2E p95"
          placeholder="2s"
          value={text(["slos", "e2e_p95"])}
          onChange={(e) => edit(["slos", "e2e_p95"], e.currentTarget.value)}
          w={160}
        />
      </Group>
      <Text size="xs" c="dimmed">
        Everything else (a custom workload, Prometheus, knobs, analysis settings) is on the YAML tab; it is the same
        document.
      </Text>
    </Stack>
  );
}

function Stages({
  stages,
  edit,
}: {
  stages: { rate: number; duration: string }[];
  edit: (path: Path, value: unknown) => void;
}) {
  const set = (next: { rate: number; duration: string }[]) => edit(["load", "stages"], next);
  return (
    <Stack gap={4}>
      <Text size="sm" fw={500}>
        Stages
      </Text>
      <Table verticalSpacing={4} withRowBorders={false}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>RPS</Table.Th>
            <Table.Th>Duration</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {stages.map((stage, index) => (
            <Table.Tr key={index}>
              <Table.Td>
                <NumberInput
                  aria-label={`Stage ${index} RPS`}
                  min={0}
                  value={stage.rate}
                  onChange={(value) =>
                    set(stages.map((s, i) => (i === index ? { ...s, rate: Number(value) || 0 } : s)))
                  }
                  w={140}
                />
              </Table.Td>
              <Table.Td>
                <TextInput
                  aria-label={`Stage ${index} duration`}
                  value={stage.duration}
                  onChange={(e) => {
                    const duration = e.currentTarget.value;
                    set(stages.map((s, i) => (i === index ? { ...s, duration } : s)));
                  }}
                  w={140}
                />
              </Table.Td>
              <Table.Td>
                <ActionIcon
                  variant="subtle"
                  color="gray"
                  aria-label={`Remove stage ${index}`}
                  onClick={() => set(stages.filter((_, i) => i !== index))}
                >
                  ×
                </ActionIcon>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Group>
        <Button
          size="xs"
          variant="subtle"
          onClick={() =>
            set([...stages, { rate: (stages.at(-1)?.rate ?? 5) * 2, duration: stages.at(-1)?.duration ?? "60s" }])
          }
        >
          Add a stage
        </Button>
      </Group>
    </Stack>
  );
}

function Actions({ text, document }: { text: string; document: Document }) {
  const validate = useValidateSpec();
  const create = useCreateRun();
  const navigate = useNavigate();
  const parses = document.errors.length === 0;
  const report = validate.data;
  return (
    <Stack gap="sm">
      <Group>
        <Button variant="default" loading={validate.isPending} disabled={!parses} onClick={() => validate.mutate(text)}>
          Validate
        </Button>
        <Button
          loading={create.isPending}
          disabled={!parses}
          onClick={() => create.mutate(document.toJSON(), { onSuccess: (run) => navigate(`/runs/${run.id}`) })}
        >
          Create run
        </Button>
      </Group>
      {validate.isError && (
        <Text size="sm" c="red" role="alert">
          {validate.error.message}
        </Text>
      )}
      {report && (
        <Alert
          color={!report.valid ? "red" : report.warnings.length ? "yellow" : "teal"}
          title={report.valid ? (report.warnings.length ? "Valid, with warnings" : "Valid") : "Not valid"}
        >
          {[...report.errors, ...report.warnings].length > 0 && (
            <List size="sm">
              {report.errors.map((problem) => (
                <List.Item key={problem}>{problem}</List.Item>
              ))}
              {report.warnings.map((warning) => (
                <List.Item key={warning}>Warning: {warning}</List.Item>
              ))}
            </List>
          )}
        </Alert>
      )}
      {create.isError && (
        <Text size="sm" c="red" role="alert">
          {create.error.message}
        </Text>
      )}
    </Stack>
  );
}
