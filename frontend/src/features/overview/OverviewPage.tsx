import { Anchor, Badge, Button, Group, Loader, Paper, SimpleGrid, Stack, Table, Text, Title } from "@mantine/core";
import { Link } from "react-router";
import { useManagedTargets, useRuns, useTuningSessions } from "../../api/hooks";
import type { ManagedTarget, RunResource, TuningSession } from "../../api/types";
import { formatNumber } from "../../components/format";
import { QueryError } from "../../components/QueryError";
import { StatusBadge } from "../../components/StatusBadge";
import { TimeValue } from "../../components/TimeValue";
import { deploymentState, placementLine } from "../managed-targets/summary";
import { ADD_HOST_FOR_TUNING, describeStep, isActive, STATUS_BADGE } from "../tuning/session";
import { Allowed } from "../auth/Allowed";

// The newest runs and sessions the home page shows; the full lists are a click away.
const RECENT_RUNS = 5;
const RECENT_SESSIONS = 3;

/**
 * The home page: what servtuner deploys and what it is doing. Managed servers with their state, the
 * tuning sessions (active ones first), and the newest runs, each linking to its own page.
 */
export function OverviewPage() {
  return (
    <Stack gap="lg">
      <Title order={2}>Overview</Title>
      <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="lg">
        <ManagedVllms />
        <Sessions />
      </SimpleGrid>
      <RecentRuns />
    </Stack>
  );
}

function Section({
  title,
  to,
  action,
  children,
}: {
  title: string;
  to: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Paper withBorder p="md" radius="sm">
      <Stack gap="sm">
        <Group justify="space-between">
          <Anchor component={Link} to={to} fw={600} size="lg">
            {title}
          </Anchor>
          {action}
        </Group>
        {children}
      </Stack>
    </Paper>
  );
}

function ManagedVllms() {
  const targets = useManagedTargets();
  return (
    <Section
      title="Managed Servers"
      to="/managed-targets"
      action={
        <Group gap="xs">
          <Button component={Link} to="/managed-targets?add=kubernetes" size="xs" variant="default">
            Add on Kubernetes
          </Button>
          <Button component={Link} to={ADD_HOST_FOR_TUNING} size="xs" variant="default">
            Add a vLLM host
          </Button>
        </Group>
      }
    >
      {targets.isPending && <Loader size="sm" aria-label="Loading managed targets" />}
      {targets.isError && <QueryError title="Could not load managed targets" error={targets.error} />}
      {targets.isSuccess && targets.data.length === 0 && (
        <Text size="sm" c="dimmed">
          None yet: add a vLLM for servtuner to deploy, benchmark and tune.
        </Text>
      )}
      {targets.isSuccess && targets.data.map((target) => <ManagedVllmRow key={target.id} target={target} />)}
    </Section>
  );
}

function ManagedVllmRow({ target }: { target: ManagedTarget }) {
  const state = deploymentState(target);
  return (
    <Group justify="space-between" wrap="nowrap">
      <div>
        <Anchor component={Link} to={`/managed-targets/${target.id}`}>
          {target.name}
        </Anchor>
        <Text size="xs" c="dimmed">
          {placementLine(target)} · {target.spec.model}
        </Text>
      </div>
      <Group gap="xs" wrap="nowrap">
        <Badge variant="light" color={state.color}>
          {state.label}
        </Badge>
        {state.since && <TimeValue timestamp={state.since} />}
      </Group>
    </Group>
  );
}

function Sessions() {
  const sessions = useTuningSessions();
  const targets = useManagedTargets();
  const targetName = new Map((targets.data ?? []).map((target) => [target.id, target.name]));
  // Active sessions first (there is at most one), then the newest finished ones.
  const shown = [
    ...(sessions.data ?? []).filter(isActive),
    ...(sessions.data ?? []).filter((session) => !isActive(session)),
  ].slice(0, RECENT_SESSIONS);
  return (
    <Section
      title="Tuning"
      to="/tuning"
      action={
        <Button component={Link} to="/tuning/new" size="xs" variant="default" disabled={!targets.data?.length}>
          New session
        </Button>
      }
    >
      {sessions.isPending && <Loader size="sm" aria-label="Loading sessions" />}
      {sessions.isError && <QueryError title="Could not load sessions" error={sessions.error} />}
      {sessions.isSuccess && shown.length === 0 && (
        <Text size="sm" c="dimmed">
          No sessions yet: a session finds the best vLLM flags for a workload and SLOs.
        </Text>
      )}
      {shown.map((session) => (
        <SessionRow key={session.id} session={session} target={targetName.get(session.managed_target_id)} />
      ))}
    </Section>
  );
}

function SessionRow({ session, target }: { session: TuningSession; target: string | undefined }) {
  const badge = STATUS_BADGE[session.status];
  return (
    <Group justify="space-between" wrap="nowrap">
      <div>
        <Anchor component={Link} to={`/tuning/${session.id}`}>
          {session.experiment}
        </Anchor>
        <Text size="xs" c="dimmed">
          {target ?? session.managed_target_id} · {session.request.preset} ·{" "}
          {isActive(session)
            ? describeStep(session.step as Parameters<typeof describeStep>[0])
            : `${session.runs_started} runs`}
        </Text>
      </div>
      <Badge variant="light" color={badge.color}>
        {badge.label}
      </Badge>
    </Group>
  );
}

function RecentRuns() {
  const runs = useRuns({ limit: RECENT_RUNS });
  return (
    <Section
      title="Recent runs"
      to="/runs"
      action={
        <Allowed action="runs.write">
          <Button component={Link} to="/runs/new" size="xs" variant="default">
            New run
          </Button>
        </Allowed>
      }
    >
      {runs.isPending && <Loader size="sm" aria-label="Loading runs" />}
      {runs.isError && <QueryError title="Could not load runs" error={runs.error} />}
      {runs.isSuccess && runs.data.length === 0 && (
        <Text size="sm" c="dimmed">
          No runs yet.
        </Text>
      )}
      {runs.isSuccess && runs.data.length > 0 && <RunsSummary runs={runs.data} />}
    </Section>
  );
}

function RunsSummary({ runs }: { runs: RunResource[] }) {
  return (
    <Table verticalSpacing={4}>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>Run</Table.Th>
          <Table.Th>Status</Table.Th>
          <Table.Th>SLO-max (RPS)</Table.Th>
          <Table.Th>Created</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {runs.map((run) => (
          <Table.Tr key={run.id}>
            <Table.Td>
              <Anchor component={Link} to={`/runs/${run.id}`}>
                {run.name}
              </Anchor>
            </Table.Td>
            <Table.Td>
              <StatusBadge status={run.status} />
            </Table.Td>
            <Table.Td>{run.slo_max_rps == null ? "–" : formatNumber(run.slo_max_rps, 3)}</Table.Td>
            <Table.Td>
              <TimeValue timestamp={run.created_at} />
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}
