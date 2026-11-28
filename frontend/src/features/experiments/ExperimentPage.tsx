import { Alert, Anchor, Badge, Button, Group, Loader, Paper, Select, Stack, Table, Text, Title } from "@mantine/core";
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { ApiError } from "../../api/client";
import { useAnalyzeExperiment, useExperiment } from "../../api/hooks";
import { DeleteExperimentButton } from "./DeleteExperimentButton";
import type { Comparison, ExperimentAnalysis, ExperimentDetail, ObjectiveName } from "../../api/types";
import { Chart } from "../../charts/Chart";
import type { ChartPalette } from "../../charts/palette";
import { useChartPalette, type ChartOption } from "../../charts/useChart";
import { formatSeconds } from "../../components/format";
import { QueryError } from "../../components/QueryError";
import { StatusBadge } from "../../components/StatusBadge";
import { TimeValue } from "../../components/TimeValue";
import { EstimateValue } from "./EstimateValue";
import { KnobValues } from "../knobs/KnobValues";
import { type KnobText, useKnobText } from "../knobs/knobText";
import { estimateOf, formatObjective, knobLabel, OBJECTIVE_LABELS } from "./objectives";
import { PresetName } from "../../components/PresetName";
import { Allowed } from "../auth/Allowed";

const DEFAULT_OBJECTIVE = "default";

export function ExperimentPage() {
  const { name = "" } = useParams();
  const experiment = useExperiment(name);
  const text = useKnobText();
  if (experiment.isPending) return <Loader aria-label="Loading experiment" />;
  if (experiment.isError) {
    const missing = experiment.error instanceof ApiError && experiment.error.status === 404;
    return (
      <Stack>
        <QueryError
          title={missing ? "Experiment not found" : "Could not load the experiment"}
          error={missing ? new Error(`No experiment is named ${name}.`) : experiment.error}
        />
        <Anchor component={Link} to="/experiments">
          Back to experiments
        </Anchor>
      </Stack>
    );
  }
  const detail = experiment.data;
  return (
    <Stack gap="lg">
      <Anchor component={Link} to="/experiments" size="sm">
        All experiments
      </Anchor>
      <Group justify="space-between" align="end">
        <div>
          <Title order={2}>{detail.name}</Title>
          <Text size="sm" c="dimmed">
            {detail.run_count} {detail.run_count === 1 ? "run" : "runs"}
            {detail.varied_knobs.length > 0 && `, varying ${detail.varied_knobs.map(text.label).join(" and ")}`}
          </Text>
        </div>
        <AnalyzeControls name={detail.name} />
      </Group>
      {detail.latest_analysis ? (
        <AnalysisView analysis={detail.latest_analysis} />
      ) : (
        <Text c="dimmed" maw={600}>
          Not compared yet. Comparing groups runs with the same knobs into configurations, estimates each objective with
          a 95% interval across repeats, and names a winner only when its interval clears the runner-up's.
        </Text>
      )}
      <ExperimentRuns detail={detail} />
      <Group>
        <Allowed action="runs.delete">
          <DeleteExperimentButton name={detail.name} />
        </Allowed>
      </Group>
    </Stack>
  );
}

function AnalyzeControls({ name }: { name: string }) {
  const [objective, setObjective] = useState(DEFAULT_OBJECTIVE);
  const analyze = useAnalyzeExperiment(name);
  return (
    <Stack gap={4} align="end">
      <Group gap="xs" align="end">
        <Select
          label="Objective"
          size="sm"
          w={240}
          value={objective}
          onChange={(value) => value && setObjective(value)}
          data={[
            { value: DEFAULT_OBJECTIVE, label: "Default for the target" },
            ...Object.entries(OBJECTIVE_LABELS).map(([value, label]) => ({ value, label })),
          ]}
        />
        <Button
          loading={analyze.isPending}
          onClick={() =>
            analyze.mutate(objective === DEFAULT_OBJECTIVE ? {} : { objective: objective as ObjectiveName })
          }
        >
          Compare configurations
        </Button>
      </Group>
      {analyze.isError && (
        <Text size="sm" c="red" maw={480} ta="right" role="alert">
          {analyze.error.message}
        </Text>
      )}
    </Stack>
  );
}

function AnalysisView({ analysis }: { analysis: ExperimentAnalysis }) {
  const comparison = analysis.comparison;
  const preset = comparison.comparability.workload_preset;
  return (
    <Stack gap="lg">
      <Verdict comparison={comparison} />
      <Comparability comparison={comparison} />
      <Configurations comparison={comparison} />
      <ParetoChart comparison={comparison} />
      <OtherRankings comparison={comparison} />
      <Text size="xs" c="dimmed">
        Compared <TimeValue timestamp={analysis.created_at} /> over {analysis.run_ids.length} runs, analyzer{" "}
        {comparison.analyzer_version}.
        {preset && (
          <>
            {" "}
            Every run used <PresetName preset={`${preset.name}@${preset.version}`} />: the result holds for that
            workload.
          </>
        )}
      </Text>
    </Stack>
  );
}

/** The answer first: the knob values to apply, or why nothing won. */
function Verdict({ comparison }: { comparison: Comparison }) {
  const primary = comparison.rankings.find((ranking) => ranking.objective === comparison.objective);
  const recommendation = comparison.recommendation;
  const objective = OBJECTIVE_LABELS[comparison.objective];
  if (recommendation) {
    return (
      <Paper withBorder p="lg" radius="sm" style={{ borderColor: "var(--mantine-color-teal-6)", borderWidth: 2 }}>
        <Stack gap={6}>
          <Group gap="xs">
            <Text fw={600}>Apply</Text>
            <Badge variant="light" color={{ high: "teal", medium: "yellow", low: "gray" }[recommendation.confidence]}>
              {recommendation.confidence === "high"
                ? "High"
                : recommendation.confidence === "medium"
                  ? "Medium"
                  : "Low"}{" "}
              confidence
            </Badge>
          </Group>
          <Text size="xl" fw={700} ff="monospace">
            <KnobValues knobs={recommendation.knobs} />
          </Text>
          <Text size="sm">
            Best on {objective.toLowerCase()}. {primary?.reason}
          </Text>
        </Stack>
      </Paper>
    );
  }
  return (
    <Alert variant="light" color="gray" title={`No configuration wins on ${objective.toLowerCase()}`}>
      {primary?.reason ??
        `${objective} could not be computed for every configuration, so nothing was ranked on it. The configurations table says why.`}
    </Alert>
  );
}

function Comparability({ comparison }: { comparison: Comparison }) {
  const { comparability, excluded_runs: excluded } = comparison;
  if (comparability.issues.length === 0 && excluded.length === 0) return null;
  return (
    <Alert
      variant="light"
      color={comparability.is_comparable ? "orange" : "red"}
      title={comparability.is_comparable ? "Read with care" : "These runs are not comparable"}
    >
      <Stack gap={4}>
        {comparability.issues.map((issue) => (
          <Text key={issue.code} size="sm">
            {issue.message}
          </Text>
        ))}
        {excluded.map((run) => (
          <Text key={run.run_id} size="sm">
            Left out{" "}
            <Anchor component={Link} to={`/runs/${run.run_id}`}>
              {run.run_id}
            </Anchor>
            : {run.reason}
          </Text>
        ))}
      </Stack>
    </Alert>
  );
}

function Configurations({ comparison }: { comparison: Comparison }) {
  const winner = comparison.recommendation?.configuration_id;
  return (
    <Stack gap="xs">
      <Title order={4}>Configurations</Title>
      <Table.ScrollContainer minWidth={640}>
        <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Configuration</Table.Th>
              <Table.Th>Runs</Table.Th>
              <Table.Th>{OBJECTIVE_LABELS[comparison.objective]}</Table.Th>
              <Table.Th>
                p95 TTFT
                {comparison.reference_stage_index != null && ` at stage ${comparison.reference_stage_index}`}
              </Table.Th>
              <Table.Th>Pareto</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {comparison.configurations.map((configuration) => (
              <Table.Tr
                key={configuration.id}
                bg={configuration.id === winner ? "var(--mantine-color-teal-light)" : undefined}
              >
                <Table.Td>
                  <Text size="sm" fw={configuration.id === winner ? 600 : undefined}>
                    <KnobValues knobs={configuration.knobs} names={comparison.varied_knobs} />
                  </Text>
                  <Text size="xs" c="dimmed">
                    {configuration.id}
                  </Text>
                </Table.Td>
                <Table.Td>{configuration.run_ids.length}</Table.Td>
                <Table.Td>
                  <EstimateValue
                    objective={comparison.objective}
                    estimate={estimateOf(configuration, comparison.objective)}
                  />
                </Table.Td>
                <Table.Td>
                  <EstimateValue
                    objective="p95_ttft_at_target_rps"
                    estimate={configuration.reference_ttft_p95_seconds ?? undefined}
                  />
                </Table.Td>
                <Table.Td>{configuration.is_pareto ? "Yes" : "–"}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Stack>
  );
}

function ParetoChart({ comparison }: { comparison: Comparison }) {
  const palette = useChartPalette();
  const text = useKnobText();
  const option = useMemo(() => paretoOption(comparison, palette, text), [comparison, palette, text]);
  if (option === null) return null;
  return (
    <Stack gap="xs">
      <Title order={4}>Throughput against latency</Title>
      <Chart
        option={option}
        label={`${OBJECTIVE_LABELS[comparison.objective]} against p95 time to first token, one point per configuration`}
        height={300}
      />
      <Text size="xs" c="dimmed">
        Filled points are the Pareto set: no other configuration is better on both. p95 TTFT is read at the highest
        stage every run passed.
      </Text>
    </Stack>
  );
}

function paretoOption(comparison: Comparison, palette: ChartPalette, text: KnobText): ChartOption | null {
  const points = comparison.configurations.flatMap((configuration) => {
    const x = estimateOf(configuration, comparison.objective)?.value;
    const y = configuration.reference_ttft_p95_seconds?.value;
    return x == null || y == null ? [] : [{ configuration, x, y }];
  });
  if (points.length < 2) return null;
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 72, right: 24, top: 24, bottom: 48 },
    tooltip: {
      trigger: "item",
      formatter: (params: { data: { name: string; value: [number, number] } }) =>
        `${params.data.name}<br/>${formatObjective(comparison.objective, params.data.value[0])}, p95 TTFT ${formatSeconds(params.data.value[1])}`,
    },
    xAxis: {
      type: "value",
      scale: true,
      name: OBJECTIVE_LABELS[comparison.objective],
      nameLocation: "middle",
      nameGap: 30,
      axisLabel: { color: palette.muted },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    yAxis: {
      type: "value",
      scale: true,
      name: "p95 TTFT",
      axisLabel: { color: palette.muted, formatter: (value: number) => formatSeconds(value) },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    series: [
      {
        type: "scatter",
        symbolSize: 12,
        label: {
          show: true,
          position: "right",
          color: palette.ink,
          formatter: (params: { data: { id: string } }) => params.data.id,
        },
        data: points.map(({ configuration, x, y }) => ({
          id: configuration.id,
          name: knobLabel(text, configuration.knobs, comparison.varied_knobs),
          value: [x, y],
          itemStyle: configuration.is_pareto
            ? { color: palette.measure }
            : { color: "transparent", borderColor: palette.offered, borderWidth: 2 },
        })),
      },
    ],
  };
}

function OtherRankings({ comparison }: { comparison: Comparison }) {
  const text = useKnobText();
  const others = comparison.rankings.filter((ranking) => ranking.objective !== comparison.objective);
  if (others.length === 0) return null;
  const knobsOf = new Map(
    comparison.configurations.map((c) => [c.id, knobLabel(text, c.knobs, comparison.varied_knobs)]),
  );
  return (
    <Stack gap="xs">
      <Title order={4}>Other objectives</Title>
      <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Objective</Table.Th>
            <Table.Th>Best</Table.Th>
            <Table.Th>Result</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {others.map((ranking) => {
            const best = ranking.order[0];
            return (
              <Table.Tr key={ranking.objective}>
                <Table.Td>{OBJECTIVE_LABELS[ranking.objective]}</Table.Td>
                <Table.Td>
                  {best
                    ? `${knobsOf.get(best.configuration_id)}: ${formatObjective(ranking.objective, best.value)}`
                    : "–"}
                </Table.Td>
                <Table.Td>
                  <Text size="sm">{ranking.is_significant ? "Significant" : "Not significant"}</Text>
                  <Text size="xs" c="dimmed" maw={420}>
                    {ranking.reason}
                  </Text>
                </Table.Td>
              </Table.Tr>
            );
          })}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

function ExperimentRuns({ detail }: { detail: ExperimentDetail }) {
  const runs = detail.comparison.runs;
  const varied = detail.varied_knobs;
  return (
    <Stack gap="xs">
      <Title order={4}>Runs</Title>
      <Table.ScrollContainer minWidth={560}>
        <Table striped verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Run</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>Knobs</Table.Th>
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
                <Table.Td>
                  <Text size="sm">
                    <KnobValues knobs={run.knobs} names={varied} />
                  </Text>
                </Table.Td>
                <Table.Td>
                  <TimeValue timestamp={run.created_at} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {detail.comparison.unindexed_run_ids.length > 0 && (
        <Text size="xs" c="dimmed">
          {detail.comparison.unindexed_run_ids.length} of these runs have no analysis yet and take no part in
          comparisons.
        </Text>
      )}
    </Stack>
  );
}
