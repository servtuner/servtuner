import { ModelName } from "../../components/ModelName";
import { PresetName } from "../../components/PresetName";
import { Anchor, Code, Group, Loader, Stack, Tabs, Text, Title } from "@mantine/core";
import { lazy, Suspense } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { ApiError } from "../../api/client";
import { useManagedTarget, useRun, useRunEvents } from "../../api/hooks";
import { idFromLabel, MANAGED_TARGET_RUN_LABEL, TUNING_SESSION_LABEL } from "../../api/labels";
import { targetTypeLabel } from "../../api/runStatus";
import type { RunDetail } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { StatusBadge } from "../../components/StatusBadge";
import { TimeValue } from "../../components/TimeValue";
import { StageSummaryTable } from "./StageSummaryTable";
import { AwaitingConfigBanner } from "../followup/AwaitingConfigBanner";
import { DeleteRunButton } from "./DeleteRunButton";
import { StatusTimeline } from "./StatusTimeline";
import { RunCost } from "./RunCost";
import { CapacityTab } from "./tabs/CapacityTab";
import { ConfigTab } from "./tabs/ConfigTab";
import { FilesTab } from "./tabs/FilesTab";
import { RecommendationsTab } from "./tabs/RecommendationsTab";
import { Allowed } from "../auth/Allowed";

// The chart tabs pull in ECharts, so they load when first opened.
const LatencyTab = lazy(() =>
  import("./tabs/LatencyTab").then((module) => ({
    default: module.LatencyTab,
  })),
);
const QueueingTab = lazy(() =>
  import("./tabs/QueueingTab").then((module) => ({
    default: module.QueueingTab,
  })),
);
const ServerPanels = lazy(() =>
  import("./tabs/ServerPanels").then((module) => ({
    default: module.ServerPanels,
  })),
);
const ThroughputTab = lazy(() =>
  import("./tabs/ThroughputTab").then((module) => ({
    default: module.ThroughputTab,
  })),
);

// Run detail. The tab is in the URL (?tab=), so a link can open one.
const TABS = [
  { value: "overview", label: "Overview" },
  { value: "latency", label: "Latency" },
  { value: "throughput", label: "Throughput" },
  { value: "server", label: "Server" },
  { value: "resources", label: "Resources" },
  { value: "queueing", label: "Queueing" },
  { value: "recommendations", label: "Recommendations" },
  { value: "capacity", label: "Capacity" },
  { value: "config", label: "Config" },
  { value: "files", label: "Files" },
] as const;
type TabValue = (typeof TABS)[number]["value"];

function isTab(value: string | null): value is TabValue {
  return TABS.some((tab) => tab.value === value);
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <Group gap="xs" wrap="nowrap" align="baseline">
      <Text size="sm" c="dimmed" w={120}>
        {label}
      </Text>
      <Text size="sm" component="div">
        {children}
      </Text>
    </Group>
  );
}

// Knobs and labels are plain values. The API's run document carries no
// RunSpec, so no Secret reference can appear here (frontend/CLAUDE.md).
function KeyValues({ entries }: { entries: Record<string, string | number | boolean> }) {
  const pairs = Object.entries(entries);
  if (pairs.length === 0) return <>-</>;
  return (
    <>
      {pairs.map(([key, value]) => (
        <Code key={key} mr="xs">
          {key}={String(value)}
        </Code>
      ))}
    </>
  );
}

function RunMetadata({ run }: { run: RunDetail }) {
  return (
    <Stack gap="xs">
      <Field label="Status">
        <StatusBadge status={run.status} />
        {run.phase && <> phase: {run.phase}</>}
      </Field>
      <Field label="ID">
        <Code>{run.id}</Code>
      </Field>
      <Field label="Experiment">
        {run.experiment ? (
          <Anchor component={Link} to={`/experiments/${encodeURIComponent(run.experiment)}`}>
            {run.experiment}
          </Anchor>
        ) : (
          "-"
        )}
      </Field>
      <MadeBy labels={run.labels} />
      <Field label="Model">
        <ModelName model={run.model} />
      </Field>
      <Field label="Workload">
        {run.workload_preset ? <PresetName preset={run.workload_preset} /> : "Custom (see the Config tab)"}
      </Field>
      <Field label="Target type">{targetTypeLabel(run.target_type)}</Field>
      <Field label="Created">
        <TimeValue timestamp={run.created_at} />
      </Field>
      <Field label="Started">
        <TimeValue timestamp={run.started_at} />
      </Field>
      <Field label="Finished">
        <TimeValue timestamp={run.finished_at} />
      </Field>
      <Field label="Knobs">
        <KeyValues entries={run.knobs} />
      </Field>
      {run.status === "succeeded" && (
        <Field label="Cost">
          <RunCost runId={run.id} status={run.status} />
        </Field>
      )}
      <Field label="Labels">
        <KeyValues entries={run.labels} />
      </Field>
      <Field label="Spec hash">
        <Code>{run.spec_hash}</Code>
      </Field>
      {run.failure_message && (
        <Field label="Failure">
          {run.failure_message}
          {run.exit_code != null && <> (exit code {run.exit_code})</>}
        </Field>
      )}
    </Stack>
  );
}

export function RunOverviewPage() {
  const { runId = "" } = useParams();
  const run = useRun(runId);
  const events = useRunEvents(runId, run.data?.status);
  const [searchParams, setSearchParams] = useSearchParams();
  const requested = searchParams.get("tab");
  const tab: TabValue = isTab(requested) ? requested : "overview";

  if (run.isPending) return <Loader aria-label="Loading run" />;
  if (run.isError) {
    const isNotFound = run.error instanceof ApiError && run.error.status === 404;
    return (
      <Stack>
        {isNotFound ? (
          <QueryError title="Run not found" error={new Error(`No run has the ID ${runId}.`)} />
        ) : (
          <QueryError title="Could not load run" error={run.error} />
        )}
        <Anchor component={Link} to="/runs">
          Back to runs
        </Anchor>
      </Stack>
    );
  }

  const status = run.data.status;
  return (
    <Stack>
      <Anchor component={Link} to="/runs" size="sm">
        All runs
      </Anchor>
      <Group justify="space-between" align="center">
        <Group gap="sm" align="center">
          <Title order={2}>{run.data.name}</Title>
          <ModelName model={run.data.model} size="lg" />
          <StatusBadge status={status} />
        </Group>
        <Group gap="md">
          <Anchor component={Link} to={`/runs/new?from=${encodeURIComponent(runId)}`} size="sm">
            Clone this run
          </Anchor>
          <Anchor component={Link} to={`/schedules?from=${encodeURIComponent(runId)}`} size="sm">
            Schedule this spec
          </Anchor>
        </Group>
      </Group>
      <AwaitingConfigBanner run={run.data} />
      <Tabs
        value={tab}
        onChange={(value) =>
          setSearchParams(value === "overview" || value === null ? {} : { tab: value }, { replace: true })
        }
        keepMounted={false}
      >
        <Tabs.List mb="md">
          {TABS.map((entry) => (
            <Tabs.Tab key={entry.value} value={entry.value}>
              {entry.label}
            </Tabs.Tab>
          ))}
        </Tabs.List>
        <Tabs.Panel value="overview">
          <Overview run={run.data} events={events} />
        </Tabs.Panel>
        <Tabs.Panel value="latency">
          <Suspense fallback={<Loader size="sm" aria-label="Loading charts" />}>
            <LatencyTab runId={runId} status={status} />
          </Suspense>
        </Tabs.Panel>
        <Tabs.Panel value="throughput">
          <Suspense fallback={<Loader size="sm" aria-label="Loading charts" />}>
            <ThroughputTab runId={runId} status={status} />
          </Suspense>
        </Tabs.Panel>
        <Tabs.Panel value="server">
          <Suspense fallback={<Loader size="sm" aria-label="Loading charts" />}>
            <ServerPanels runId={runId} status={status} group="server" />
          </Suspense>
        </Tabs.Panel>
        <Tabs.Panel value="resources">
          <Suspense fallback={<Loader size="sm" aria-label="Loading charts" />}>
            <ServerPanels runId={runId} status={status} group="resources" />
          </Suspense>
        </Tabs.Panel>
        <Tabs.Panel value="queueing">
          <Suspense fallback={<Loader size="sm" aria-label="Loading charts" />}>
            <QueueingTab runId={runId} status={status} />
          </Suspense>
        </Tabs.Panel>
        <Tabs.Panel value="recommendations">
          <RecommendationsTab runId={runId} status={status} />
        </Tabs.Panel>
        <Tabs.Panel value="capacity">
          <CapacityTab runId={runId} status={status} />
        </Tabs.Panel>
        <Tabs.Panel value="config">
          <ConfigTab runId={runId} status={status} />
        </Tabs.Panel>
        <Tabs.Panel value="files">
          <FilesTab runId={runId} status={status} />
        </Tabs.Panel>
      </Tabs>
      <Group>
        <Allowed action="runs.delete">
          <DeleteRunButton runId={runId} name={run.data.name} />
        </Allowed>
      </Group>
    </Stack>
  );
}

function Overview({ run, events }: { run: RunDetail; events: ReturnType<typeof useRunEvents> }) {
  return (
    <Stack>
      <RunMetadata run={run} />
      <Title order={3}>Status timeline</Title>
      {events.isPending && <Loader aria-label="Loading events" />}
      {events.isError && <QueryError title="Could not load events" error={events.error} />}
      {events.isSuccess && <StatusTimeline events={events.data} />}
      {run.summary && (
        <>
          <Title order={3}>Summary</Title>
          <StageSummaryTable summary={run.summary} />
        </>
      )}
    </Stack>
  );
}

/** The managed vLLM a run measured and the tuning session that started it, from its labels. */
function MadeBy({ labels }: { labels: Record<string, string> }) {
  const targetId = idFromLabel(labels[MANAGED_TARGET_RUN_LABEL]);
  const sessionId = idFromLabel(labels[TUNING_SESSION_LABEL]);
  return (
    <>
      {targetId && (
        <Field label="Managed vLLM">
          <ManagedTargetLink targetId={targetId} />
        </Field>
      )}
      {sessionId && (
        <Field label="Tuning session">
          <Anchor component={Link} to={`/tuning/${sessionId}`}>
            {sessionId}
          </Anchor>
        </Field>
      )}
    </>
  );
}

function ManagedTargetLink({ targetId }: { targetId: string }) {
  const target = useManagedTarget(targetId);
  return (
    <Anchor component={Link} to={`/managed-targets/${targetId}`}>
      {target.data?.name ?? (target.isError ? `${targetId} (deleted)` : targetId)}
    </Anchor>
  );
}
