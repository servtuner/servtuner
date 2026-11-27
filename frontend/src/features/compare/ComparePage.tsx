import { Alert, Anchor, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";
import { useRunComparison } from "../../api/hooks";
import type { RunComparison } from "../../api/types";
import { Chart } from "../../charts/Chart";
import type { ChartPalette } from "../../charts/palette";
import { useChartPalette, type ChartOption } from "../../charts/useChart";
import { formatNumber, formatPercent, formatSeconds, formatSecondsRange, formatUsd } from "../../components/format";
import { QueryError } from "../../components/QueryError";
import { KnobName, KnobValues } from "../knobs/KnobValues";
import { useKnobText } from "../knobs/knobText";
import { separatedLowest } from "./separation";

type Metric = RunComparison["metrics"][number];
type Cells = Map<string, Metric>;

const key = (stage: string, metric: string, stat: string) => `${stage}/${metric}/${stat}`;

/** The comparison: the knob diff first, then aligned metrics, then overlays. */
export function ComparePage() {
  const [searchParams] = useSearchParams();
  const runIds = (searchParams.get("runs") ?? "").split(",").filter(Boolean);
  const comparison = useRunComparison(runIds);
  if (runIds.length === 0) {
    return (
      <Text c="dimmed">
        Choose runs to compare on the{" "}
        <Anchor component={Link} to="/runs">
          runs list
        </Anchor>
        .
      </Text>
    );
  }
  if (comparison.isPending) return <Loader aria-label="Loading comparison" />;
  if (comparison.isError) return <QueryError title="Could not compare these runs" error={comparison.error} />;
  return <Comparison comparison={comparison.data} />;
}

function Comparison({ comparison }: { comparison: RunComparison }) {
  const cells: Cells = useMemo(
    () => new Map(comparison.metrics.map((metric) => [key(metric.stage, metric.metric, metric.stat), metric])),
    [comparison],
  );
  const stages = [...new Set(comparison.metrics.map((metric) => metric.stage).filter((stage) => stage !== "run"))]
    .map(Number)
    .sort((a, b) => a - b);
  return (
    <Stack gap="lg">
      <Title order={2}>Comparing {comparison.runs.length} runs</Title>
      <GuardRails comparison={comparison} />
      <KnobDiff comparison={comparison} />
      <RunLevel comparison={comparison} cells={cells} />
      <LatencyOverlay comparison={comparison} cells={cells} stages={stages} />
      {stages.map((stage) => (
        <StageTable key={stage} comparison={comparison} cells={cells} stage={String(stage)} />
      ))}
    </Stack>
  );
}

function RunHeads({ comparison }: { comparison: RunComparison }) {
  return (
    <>
      {comparison.runs.map((run) => (
        <Table.Th key={run.id}>
          <Anchor component={Link} to={`/runs/${run.id}`} size="sm">
            {run.name}
          </Anchor>
        </Table.Th>
      ))}
    </>
  );
}

function GuardRails({ comparison }: { comparison: RunComparison }) {
  const notes: string[] = [];
  if (!comparison.ladders_match) {
    notes.push(
      "The runs offered different load at some stage, so a stage row compares unlike loads. Use the run-level rows and the overlay instead.",
    );
  }
  if (comparison.unindexed_run_ids.length > 0) {
    const names = comparison.runs.filter((run) => comparison.unindexed_run_ids.includes(run.id)).map((run) => run.name);
    notes.push(`No analysis yet for ${names.join(", ")}: those columns are empty.`);
  }
  if (notes.length === 0) return null;
  return (
    <Alert variant="light" color="orange" title="Read with care">
      <Stack gap={4}>
        {notes.map((note) => (
          <Text key={note} size="sm">
            {note}
          </Text>
        ))}
      </Stack>
    </Alert>
  );
}

function KnobDiff({ comparison }: { comparison: RunComparison }) {
  const differing = comparison.knobs.filter((knob) => knob.differs);
  const same = comparison.knobs.filter((knob) => !knob.differs);
  const text = useKnobText();
  return (
    <Stack gap="xs">
      <Title order={4}>What differs</Title>
      {differing.length === 0 ? (
        <Text size="sm" c="dimmed">
          The runs declare the same knobs: any difference below is run-to-run variation.
        </Text>
      ) : (
        <Table verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Knob</Table.Th>
              <RunHeads comparison={comparison} />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {differing.map((knob) => (
              <Table.Tr key={knob.name}>
                <Table.Td>
                  <KnobName name={knob.name} />
                </Table.Td>
                {knob.values.map((value, index) => (
                  <Table.Td key={index}>
                    {value == null ? (
                      <Text c="dimmed" span>
                        unset
                      </Text>
                    ) : (
                      text.value(knob.name, value)
                    )}
                  </Table.Td>
                ))}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {same.length > 0 && (
        <Text size="xs" c="dimmed">
          Same in every run: <KnobValues knobs={Object.fromEntries(same.map((knob) => [knob.name, knob.values[0]]))} />
        </Text>
      )}
    </Stack>
  );
}

const RUN_ROWS: {
  metric: string;
  label: string;
  format: (value: number) => string;
}[] = [
  {
    metric: "slo_max_rps",
    label: "SLO-max RPS",
    format: (value) => formatNumber(value),
  },
  {
    metric: "rps_per_replica",
    label: "RPS per replica",
    format: (value) => formatNumber(value),
  },
  {
    metric: "rps_per_gpu",
    label: "RPS per GPU",
    format: (value) => formatNumber(value),
  },
  {
    metric: "infra_usd_per_replica_hour",
    label: "Cost per replica-hour",
    format: (value) => formatUsd(value),
  },
  {
    metric: "infra_usd_per_1k_requests",
    label: "Infrastructure cost per 1k requests",
    format: (value) => formatUsd(value),
  },
  {
    metric: "infra_usd_per_1m_output_tokens",
    label: "Infrastructure cost per 1M output tokens",
    format: (value) => formatUsd(value),
  },
  {
    metric: "token_usd_per_1k_requests",
    label: "Token cost per 1k requests",
    format: (value) => formatUsd(value),
  },
  {
    metric: "token_usd_per_1m_output_tokens",
    label: "Token cost per 1M output tokens",
    format: (value) => formatUsd(value),
  },
  {
    metric: "knee_stage_index",
    label: "Knee at stage",
    format: (value) => String(value),
  },
  {
    metric: "is_excluded_from_comparison",
    label: "Excluded by the quality gate",
    format: (value) => (value ? "Yes" : "No"),
  },
];

function RunLevel({ comparison, cells }: { comparison: RunComparison; cells: Cells }) {
  const rows = RUN_ROWS.filter((row) => cells.has(key("run", row.metric, "value")));
  if (rows.length === 0) return null;
  return (
    <Stack gap="xs">
      <Title order={4}>Capacity</Title>
      <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th />
            <RunHeads comparison={comparison} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {rows.map((row) => (
            <Table.Tr key={row.metric}>
              <Table.Td>{row.label}</Table.Td>
              {cells.get(key("run", row.metric, "value"))!.values.map((value, index) => (
                <Table.Td key={index}>{value == null ? "–" : row.format(value)}</Table.Td>
              ))}
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Text size="xs" c="dimmed">
        One run per column, with no interval across runs: to call a winner, compare them as an experiment with repeats.
      </Text>
    </Stack>
  );
}

function StageTable({ comparison, cells, stage }: { comparison: RunComparison; cells: Cells; stage: string }) {
  const values = (metric: string, stat = "value") =>
    cells.get(key(stage, metric, stat))?.values ?? comparison.runs.map(() => null);
  const offered = values("offered_rps");
  const latencyRow = (metric: string, label: string) => {
    const point = values(metric, "p95");
    const lows = values(metric, "p95_ci_low");
    const highs = values(metric, "p95_ci_high");
    const best = separatedLowest(lows, highs);
    return (
      <Table.Tr>
        <Table.Td>{label}</Table.Td>
        {point.map((value, index) => (
          <Table.Td key={index} fw={index === best ? 700 : undefined} c={index === best ? "teal.8" : undefined}>
            {value == null ? "–" : formatSeconds(value)}{" "}
            <Text span size="xs" c="dimmed">
              {formatSecondsRange(lows[index], highs[index])}
            </Text>
          </Table.Td>
        ))}
      </Table.Tr>
    );
  };
  return (
    <Stack gap="xs">
      <Title order={5}>
        Stage {stage}
        {comparison.ladders_match && offered[0] != null ? `, ${formatNumber(offered[0])} RPS offered` : ""}
      </Title>
      <Table verticalSpacing={4} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th />
            <RunHeads comparison={comparison} />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          <Table.Tr>
            <Table.Td>Achieved RPS</Table.Td>
            {values("achieved_rps").map((value, index) => (
              <Table.Td key={index}>{formatNumber(value)}</Table.Td>
            ))}
          </Table.Tr>
          <Table.Tr>
            <Table.Td>Valid</Table.Td>
            {values("is_valid").map((value, index) => (
              <Table.Td key={index} c={value === 0 ? "red.7" : undefined}>
                {value == null ? "–" : value ? "Yes" : "No"}
              </Table.Td>
            ))}
          </Table.Tr>
          {latencyRow("ttft_seconds", "TTFT p95")}
          {latencyRow("e2e_seconds", "E2E p95")}
          <Table.Tr>
            <Table.Td>Error rate</Table.Td>
            {values("error_rate").map((value, index) => (
              <Table.Td key={index}>{formatPercent(value)}</Table.Td>
            ))}
          </Table.Tr>
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

function LatencyOverlay({ comparison, cells, stages }: { comparison: RunComparison; cells: Cells; stages: number[] }) {
  const palette = useChartPalette();
  const option = useMemo(() => overlayOption(comparison, cells, stages, palette), [comparison, cells, stages, palette]);
  if (option === null) return null;
  return (
    <Stack gap="xs">
      <Title order={4}>p95 TTFT against achieved RPS</Title>
      <Chart option={option} label="p95 time to first token against achieved requests per second, one line per run" />
    </Stack>
  );
}

function overlayOption(
  comparison: RunComparison,
  cells: Cells,
  stages: number[],
  palette: ChartPalette,
): ChartOption | null {
  // Each run's line joins its valid stages; invalid ones (usually saturated, so achieving less than
  // offered) are hollow points beside it, or the line would run backwards. A series of the same
  // name takes the same colour and legend entry.
  const points = (index: number, valid: boolean) =>
    stages.flatMap((stage) => {
      const x = cells.get(key(String(stage), "achieved_rps", "value"))?.values[index];
      const y = cells.get(key(String(stage), "ttft_seconds", "p95"))?.values[index];
      const isValid = cells.get(key(String(stage), "is_valid", "value"))?.values[index];
      return x == null || y == null || (isValid === 0) === valid ? [] : [[x, y]];
    });
  const series = comparison.runs.flatMap((run, index) => [
    { name: run.name, type: "line", data: points(index, true) },
    {
      name: run.name,
      type: "scatter",
      symbol: "emptyCircle",
      symbolSize: 7,
      data: points(index, false),
    },
  ]);
  if (series.every((line) => line.data.length === 0)) return null;
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 64, right: 16, top: 36, bottom: 40 },
    legend: { top: 0, right: 0, textStyle: { color: palette.muted } },
    tooltip: {
      trigger: "item",
      valueFormatter: (value: number) => formatSeconds(value),
    },
    xAxis: {
      type: "value",
      name: "achieved RPS",
      nameLocation: "middle",
      nameGap: 26,
      axisLabel: { color: palette.muted },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    yAxis: {
      type: "value",
      name: "seconds",
      axisLabel: {
        color: palette.muted,
        formatter: (value: number) => formatSeconds(value),
      },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    series,
  };
}
