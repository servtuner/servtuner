import {
  Alert,
  Anchor,
  Badge,
  Button,
  Code,
  CopyButton,
  Group,
  Loader,
  Paper,
  Stack,
  Table,
  Text,
  Title,
} from "@mantine/core";
import { Link, useNavigate, useParams } from "react-router";
import { useManagedTarget, useManagedTargetActions, useTuningSession, useTuningSessionActions } from "../../api/hooks";
import type { TuningSession, VllmKnobs } from "../../api/types";
import { PresetName } from "../../components/PresetName";
import { formatNumber, formatSeconds } from "../../components/format";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { STATUS_BADGE, describeStep, isActive } from "./session";
import { KnobValues } from "../knobs/KnobValues";

type Recommendation = {
  command: string;
  /** The winner's knobs, as POST /managed-targets/{id}/deploy takes them. */
  knobs: VllmKnobs;
  confirmed: boolean;
  evidence: {
    confidence?: string;
    is_significant?: boolean;
    objective_value?: number;
    run_ids?: string[];
  };
  infeasible: { knobs: Record<string, unknown>; reason?: string }[];
  /** servtuner propose's findings, such as utilizations that booted the same configuration. */
  notes?: string[];
  /** How vLLM sized the winner's KV cache, when it logged it (vLLM 0.34+). */
  kv_sizing?: {
    bound: "memory" | "utilization";
    kv_cache_gib: number;
    requested_gib: number;
  } | null;
};
type HistoryEntry = {
  action: string;
  config_id: string;
  knobs: Record<string, unknown>;
  rationale?: { kind?: string; text?: string };
  run_ids?: string[];
  boot_result?: { outcome: string; c_kv?: number; reason?: string };
};

/** One tuning session: what it is doing, what it tried, and the command line it recommends. */
export function SessionPage() {
  const { sessionId = "" } = useParams();
  const session = useTuningSession(sessionId);
  if (session.isPending) return <Loader size="sm" aria-label="Loading the session" />;
  if (session.isError) return <QueryError title="Could not load the session" error={session.error} />;
  return <SessionView session={session.data} />;
}

function SessionView({ session }: { session: TuningSession }) {
  const actions = useTuningSessionActions(session.id);
  const outcome = (session.outcome ?? {}) as {
    reason?: string;
    outcome?: string;
    recommendation?: Recommendation;
    pareto?: ParetoConfiguration[];
    comparison?: { objective: string };
    pareto_error?: string;
  };
  const history = session.history as HistoryEntry[];
  return (
    <Stack gap="md">
      <Anchor component={Link} to="/tuning" size="sm">
        Tuning
      </Anchor>
      <Group justify="space-between">
        <Group gap="sm">
          <Title order={2}>{session.experiment}</Title>
          <Badge variant="light" color={STATUS_BADGE[session.status].color}>
            {STATUS_BADGE[session.status].label}
          </Badge>
          <PresetName preset={session.request.preset} />
        </Group>
        <Group>
          {session.status === "awaiting_approval" && (
            <Button onClick={() => actions.approve.mutate()} loading={actions.approve.isPending}>
              Run this step
            </Button>
          )}
          {isActive(session) && (
            <Button
              variant="default"
              color="red"
              onClick={() => actions.cancel.mutate()}
              loading={actions.cancel.isPending}
            >
              Cancel session
            </Button>
          )}
        </Group>
      </Group>
      <Text size="sm">
        Tunes <TargetLink targetId={session.managed_target_id} />.{" "}
        {describeStep(session.step as Parameters<typeof describeStep>[0])}. {session.runs_started} of{" "}
        {session.request.budget.max_runs} runs used. Started <TimeValue timestamp={session.created_at} />.{" "}
        <Anchor component={Link} to={`/experiments/${encodeURIComponent(session.experiment)}`}>
          Its experiment
        </Anchor>
      </Text>
      {outcome.recommendation && (
        <RecommendationCard
          recommendation={outcome.recommendation}
          // The api refuses a deploy while the session holds the target (backlog U1).
          deployTo={isActive(session) ? undefined : session.managed_target_id}
        />
      )}
      {(outcome.pareto || outcome.pareto_error) && (
        <ParetoCard
          pareto={outcome.pareto ?? []}
          objective={outcome.comparison?.objective}
          error={outcome.pareto_error}
          experiment={session.experiment}
        />
      )}
      {!outcome.recommendation && outcome.reason && (
        <Alert
          color={session.status === "failed" ? "red" : "gray"}
          title={session.status === "failed" ? "The session failed" : "Ended"}
        >
          {outcome.reason}
        </Alert>
      )}
      <Title order={4}>Steps</Title>
      {history.length === 0 ? (
        <Text size="sm" c="dimmed">
          No step finished yet.
        </Text>
      ) : (
        <Table verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Configuration</Table.Th>
              <Table.Th>Knobs</Table.Th>
              <Table.Th>Why</Table.Th>
              <Table.Th>Result</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {history.map((entry, index) => (
              <Table.Tr key={index}>
                <Table.Td ff="monospace">{entry.config_id}</Table.Td>
                <Table.Td fz="xs">
                  <KnobValues knobs={entry.knobs} />
                </Table.Td>
                <Table.Td fz="sm">{entry.rationale?.text ?? entry.rationale?.kind ?? "–"}</Table.Td>
                <Table.Td fz="sm">{resultOf(entry)}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}

function resultOf(entry: HistoryEntry): string {
  const boot = entry.boot_result;
  if (boot?.outcome === "infeasible") return `Did not boot: ${boot.reason ?? "no reason recorded"}`;
  if (boot?.outcome === "feasible") return `Boots; the KV cache holds about ${Math.floor(boot.c_kv ?? 0)} requests`;
  if (entry.run_ids?.length) return `${entry.run_ids.length} run${entry.run_ids.length === 1 ? "" : "s"}`;
  return "–";
}

function RecommendationCard({
  recommendation,
  deployTo,
}: {
  recommendation: Recommendation;
  deployTo: string | undefined;
}) {
  return (
    <Paper withBorder p="lg" radius="sm" style={{ borderColor: "var(--mantine-color-teal-6)", borderWidth: 2 }}>
      <Stack gap={8}>
        <Group gap="xs">
          <Text fw={600}>Recommended command line</Text>
          <Badge variant="light" color={badgeOf(recommendation).color}>
            {badgeOf(recommendation).label}
          </Badge>
        </Group>
        <Group gap="xs" align="start" wrap="nowrap">
          <Code block style={{ flex: 1 }}>
            {recommendation.command}
          </Code>
          <CopyButton value={recommendation.command}>
            {({ copied, copy }) => (
              <Button variant="default" size="xs" onClick={copy}>
                {copied ? "Copied" : "Copy"}
              </Button>
            )}
          </CopyButton>
        </Group>
        {deployTo && <DeployRecommendation targetId={deployTo} knobs={recommendation.knobs} />}
        {recommendation.kv_sizing && (
          <Text size="sm">
            KV cache: {recommendation.kv_sizing.kv_cache_gib} GiB,{" "}
            {recommendation.kv_sizing.bound === "memory"
              ? `sized by the memory free after warmup, below the ${recommendation.kv_sizing.requested_gib} GiB gpu_memory_utilization allows: a higher utilization would not add any`
              : `sized by gpu_memory_utilization (${recommendation.kv_sizing.requested_gib} GiB requested)`}
            .
          </Text>
        )}
        {(recommendation.notes ?? []).map((note) => (
          <Text key={note} size="sm" c="dimmed">
            {note}
          </Text>
        ))}
        {recommendation.infeasible.length > 0 && (
          <Text size="sm" c="dimmed">
            {recommendation.infeasible.length} configuration
            {recommendation.infeasible.length === 1 ? "" : "s"} did not boot; the steps below say why.
          </Text>
        )}
      </Stack>
    </Paper>
  );
}

/** Repeats that ran but did not separate the top configurations are not a confirmation: the
 * command line is the best mean, and the badge says the difference was within noise. */
function badgeOf(recommendation: Recommendation): {
  label: string;
  color: string;
} {
  if (!recommendation.confirmed) return { label: "Unconfirmed: the budget ran out first", color: "yellow" };
  if (recommendation.evidence.is_significant === false)
    return {
      label: "Repeated, no significant winner: the best mean, within noise of the runner-up",
      color: "yellow",
    };
  return {
    label: `Confirmed, ${recommendation.evidence.confidence ?? "medium"} confidence`,
    color: "teal",
  };
}

/** Deploys the winner on the session's managed target, then opens the target to watch it boot. */
function DeployRecommendation({ targetId, knobs }: { targetId: string; knobs: VllmKnobs }) {
  const { deploy } = useManagedTargetActions(targetId);
  const navigate = useNavigate();
  return (
    <Stack gap={4}>
      <Group>
        <Button
          size="xs"
          loading={deploy.isPending}
          onClick={() => deploy.mutate(knobs, { onSuccess: () => navigate(`/managed-targets/${targetId}`) })}
        >
          Deploy this configuration
        </Button>
      </Group>
      {deploy.isError && (
        <Text size="sm" c="red" role="alert">
          {deploy.error.message}
        </Text>
      )}
    </Stack>
  );
}

/** The managed vLLM a session tunes, by name, linked to its page (its id while it loads). */
function TargetLink({ targetId }: { targetId: string }) {
  const target = useManagedTarget(targetId);
  return (
    <Anchor component={Link} to={`/managed-targets/${targetId}`}>
      {target.data?.name ?? targetId}
    </Anchor>
  );
}

type ParetoConfiguration = {
  knobs: Record<string, unknown>;
  run_ids: string[];
  objective_value?: number | null;
  ttft_p95_seconds?: number | null;
};

/** The session's Pareto set: no configuration beats these on both the objective and p95 TTFT. */
function ParetoCard({
  pareto,
  objective,
  error,
  experiment,
}: {
  pareto: ParetoConfiguration[];
  objective: string | undefined;
  error: string | undefined;
  experiment: string;
}) {
  const fullComparison = (
    <Anchor component={Link} to={`/experiments/${encodeURIComponent(experiment)}`}>
      the full comparison
    </Anchor>
  );
  return (
    <Stack gap="xs">
      <Title order={4}>Pareto set</Title>
      {error ? (
        <Text size="sm" c="dimmed">
          No comparison of this session&apos;s runs ({error}); its experiment page can run one.
        </Text>
      ) : (
        <>
          <Text size="sm" c="dimmed">
            No other configuration was better on both {objective ?? "the objective"} and p95 TTFT; see {fullComparison}.
          </Text>
          <Table maw={900} verticalSpacing={6}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Knobs</Table.Th>
                <Table.Th>{objective ?? "Objective"}</Table.Th>
                <Table.Th>p95 TTFT</Table.Th>
                <Table.Th>Runs</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {pareto.map((configuration) => (
                <Table.Tr key={configuration.run_ids.join(",")}>
                  <Table.Td>
                    <KnobValues knobs={configuration.knobs} />
                  </Table.Td>
                  <Table.Td>{formatNumber(configuration.objective_value)}</Table.Td>
                  <Table.Td>{formatSeconds(configuration.ttft_p95_seconds)}</Table.Td>
                  <Table.Td>{configuration.run_ids.length}</Table.Td>
                </Table.Tr>
              ))}
            </Table.Tbody>
          </Table>
        </>
      )}
    </Stack>
  );
}
