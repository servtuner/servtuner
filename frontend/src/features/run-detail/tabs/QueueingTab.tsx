import { Badge, Stack, Table, Text, Title } from "@mantine/core";
import { useMemo } from "react";
import { useRunDocument } from "../../../api/hooks";
import type { InFlightChart, Recommendations, RunStatus } from "../../../api/types";
import { Chart } from "../../../charts/Chart";
import type { ChartPalette } from "../../../charts/palette";
import { STAGE_LABEL_SPACE, stageBandsMarkArea } from "../../../charts/stageBands";
import { useChartPalette, type ChartOption } from "../../../charts/useChart";
import { formatNumber, formatPercent, formatSeconds } from "../../../components/format";
import { QueueTerms } from "../../../components/QueueTerms";
import { UnitValue } from "../../../components/UnitValue";
import { WithDocument } from "./NotAnalysed";

type DerivationNode = NonNullable<Recommendations["derivation"]>["nodes"][number];

// Little's law within this error means the stage was in steady state.
const STEADY_ERROR = 0.1;

/** The Queueing tab: L(t) with C and B, Little's law per stage, the C/B derivation. */
export function QueueingTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const inflight = useRunDocument("getInFlight", runId, status);
  const recommendations = useRunDocument("getRecommendations", runId, status);
  const nodes = useMemo(
    () => new Map((recommendations.data?.derivation?.nodes ?? []).map((node) => [node.id, node])),
    [recommendations.data],
  );
  return (
    <Stack gap="xl">
      <QueueTerms />
      <WithDocument query={inflight} what="requests in flight">
        {(document) => (
          <>
            <InFlightOverTime
              chart={document}
              concurrency={nodes.get("queue.C")?.value}
              queue={nodes.get("queue.B")?.value}
              slotUntilFirstToken={nodes.get("queue.W_mean")?.label.includes("first token") ?? false}
            />
            <LittleChecks chart={document} />
          </>
        )}
      </WithDocument>
      <WithDocument query={recommendations} what="queue derivation">
        {(document) => (
          <QueueDerivation nodes={(document.derivation?.nodes ?? []).filter((node) => node.id.startsWith("queue."))} />
        )}
      </WithDocument>
    </Stack>
  );
}

function InFlightOverTime({
  chart,
  concurrency,
  queue,
  slotUntilFirstToken,
}: {
  chart: InFlightChart;
  concurrency?: number;
  queue?: number;
  slotUntilFirstToken: boolean;
}) {
  const palette = useChartPalette();
  const option = useMemo(
    () => inFlightOption(chart, palette, concurrency, queue),
    [chart, palette, concurrency, queue],
  );
  return (
    <Stack gap="xs">
      <Title order={4}>Requests in flight, L(t)</Title>
      <Chart
        option={option}
        label="Client requests in flight over time, with the recommended concurrency C, and C plus the queue size B"
      />
      <Text size="xs" c="dimmed">
        Counted from the request records each second
        {chart.bucket_seconds > 1
          ? `, ${chart.bucket_seconds} seconds per point (mean, with the minimum and maximum shaded)`
          : ""}
        .
        {concurrency != null &&
          " The dashed lines are the recommended concurrency (C) and concurrency plus queue size (C + B)."}
        {concurrency != null &&
          slotUntilFirstToken &&
          " This line counts whole requests, but streaming through Bifrost a slot is held only until the first token, so C sits below it without the router being short of slots."}
      </Text>
    </Stack>
  );
}

function inFlightOption(
  chart: InFlightChart,
  palette: ChartPalette,
  concurrency?: number,
  queue?: number,
): ChartOption {
  const bands = chart.stages.map((stage) => ({
    stage_index: stage.stage_index,
    start_offset_seconds: stage.start_offset_seconds,
    end_offset_seconds: stage.end_offset_seconds,
    offered_rps: stage.arrival_rps,
    is_valid: true,
    is_excluded: false,
  }));
  const lines =
    concurrency == null
      ? []
      : [
          {
            yAxis: concurrency,
            name: `concurrency C = ${formatNumber(concurrency)}`,
          },
          ...(queue
            ? [
                {
                  yAxis: concurrency + queue,
                  name: `C + queue size B = ${formatNumber(concurrency + queue)}`,
                },
              ]
            : []),
        ];
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 64, right: 72, top: STAGE_LABEL_SPACE, bottom: 40 },
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number) => formatNumber(value),
    },
    xAxis: {
      type: "value",
      name: "seconds since the first stage",
      nameLocation: "middle",
      nameGap: 26,
      min: 0,
      max: chart.stages.at(-1)?.end_offset_seconds,
      axisLabel: { color: palette.muted },
      splitLine: { show: false },
    },
    yAxis: {
      type: "value",
      name: "requests",
      axisLabel: { color: palette.muted },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    series: [
      {
        name: "max",
        type: "line",
        showSymbol: false,
        lineStyle: { opacity: 0 },
        areaStyle: { color: palette.band, opacity: 1 },
        data: chart.points.map((point) => [point.offset_seconds, point.max]),
        markArea: stageBandsMarkArea(bands, palette),
      },
      {
        name: "mean",
        type: "line",
        showSymbol: false,
        color: palette.measure,
        data: chart.points.map((point) => [point.offset_seconds, point.mean]),
        markLine:
          lines.length === 0
            ? undefined
            : {
                silent: true,
                symbol: "none",
                lineStyle: { type: "dashed", color: palette.warning },
                label: {
                  position: "end",
                  color: palette.warning,
                  formatter: (params: { name: string }) => params.name,
                },
                data: lines,
              },
      },
    ],
  };
}

function LittleChecks({ chart }: { chart: InFlightChart }) {
  return (
    <Stack gap="xs">
      <Title order={4}>Little's law per stage</Title>
      <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Stage</Table.Th>
            <Table.Th>Arrivals λ</Table.Th>
            <Table.Th>Mean E2E W</Table.Th>
            <Table.Th>λ × W</Table.Th>
            <Table.Th>Mean in flight L</Table.Th>
            <Table.Th>Error</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {chart.stages.map((stage) => (
            <Table.Tr key={stage.stage_index}>
              <Table.Td>{stage.stage_index}</Table.Td>
              <Table.Td>{formatNumber(stage.arrival_rps)}/s</Table.Td>
              <Table.Td>{formatSeconds(stage.mean_e2e_seconds)}</Table.Td>
              <Table.Td>
                {stage.mean_e2e_seconds == null ? "–" : formatNumber(stage.arrival_rps * stage.mean_e2e_seconds)}
              </Table.Td>
              <Table.Td>{formatNumber(stage.mean_in_flight)}</Table.Td>
              <Table.Td>
                {stage.little_error == null ? (
                  "–"
                ) : (
                  <Badge variant="light" size="sm" color={stage.little_error <= STEADY_ERROR ? "teal" : "orange"}>
                    {formatPercent(stage.little_error)}
                  </Badge>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Text size="xs" c="dimmed">
        Within {formatPercent(STEADY_ERROR)} the stage was in steady state, so the queue sizes below can be read from
        it; a larger error means the queue was still filling or draining.
      </Text>
    </Stack>
  );
}

function QueueDerivation({ nodes }: { nodes: DerivationNode[] }) {
  if (nodes.length === 0) {
    return (
      <Text size="sm" c="dimmed">
        No queue sizing: the operating stage was not in steady state, or the run has no operating point.
      </Text>
    );
  }
  return (
    <Stack gap="xs">
      <Title order={4}>How concurrency (C) and queue size (B) were sized</Title>
      <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Step</Table.Th>
            <Table.Th>How</Table.Th>
            <Table.Th ta="right">Value</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {nodes.map((node) => (
            <Table.Tr key={node.id}>
              <Table.Td>{node.label}</Table.Td>
              <Table.Td>
                {node.rendered ? (
                  <Text size="xs" ff="monospace">
                    {node.rendered}
                  </Text>
                ) : (
                  <Text size="xs" c="dimmed">
                    {node.kind === "measured" ? "measured" : node.kind === "parameter" ? "parameter" : node.kind}
                  </Text>
                )}
              </Table.Td>
              <Table.Td ta="right">
                <UnitValue value={node.value} unit={node.unit} />
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}
