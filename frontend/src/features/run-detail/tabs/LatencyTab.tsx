import { Group, SegmentedControl, Select, Stack, Table, Text, Title } from "@mantine/core";
import { useMemo, useState } from "react";
import { useRunDocument } from "../../../api/hooks";
import type { AnalysisResult, ClientTimeseries, LatencyHistograms, RunStatus } from "../../../api/types";
import { Chart } from "../../../charts/Chart";
import type { ChartPalette } from "../../../charts/palette";
import { STAGE_LABEL_SPACE, stageBandsMarkArea } from "../../../charts/stageBands";
import { useChartPalette, type ChartOption } from "../../../charts/useChart";
import { formatNumber, formatSeconds } from "../../../components/format";
import { latencyVsRpsCaption, latencyVsRpsOption } from "./latencyVsRps";
import { IntervalValue } from "../../../components/IntervalValue";
import { StageValidity } from "../../../components/StageValidity";
import { WithDocument } from "./NotAnalysed";

type Metric = "ttft_seconds" | "e2e_seconds";
const METRIC_LABELS: Record<Metric, string> = {
  ttft_seconds: "Time to first token",
  e2e_seconds: "End-to-end",
};

export function LatencyTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const analysis = useRunDocument("getAnalysis", runId, status);
  const timeseries = useRunDocument("getClientTimeseries", runId, status);
  const histograms = useRunDocument("getHistograms", runId, status);
  return (
    <Stack gap="xl">
      <WithDocument query={timeseries} what="latency over time">
        {(series) => <PercentilesOverTime series={series} />}
      </WithDocument>
      <WithDocument query={analysis} what="stage statistics">
        {(document) => (
          <>
            <StagePercentiles analysis={document} />
            <LatencyVsRps analysis={document} />
          </>
        )}
      </WithDocument>
      <WithDocument query={histograms} what="latency histograms">
        {(document) => <Histograms histograms={document} />}
      </WithDocument>
    </Stack>
  );
}

function PercentilesOverTime({ series }: { series: ClientTimeseries }) {
  const [metric, setMetric] = useState<Metric>("ttft_seconds");
  const palette = useChartPalette();
  const option = useMemo(() => percentilesOption(series, metric, palette), [series, metric, palette]);
  return (
    <Stack gap="xs">
      <Group justify="space-between" align="end">
        <Title order={4}>Latency over time</Title>
        <SegmentedControl
          size="xs"
          value={metric}
          onChange={(value) => setMetric(value as Metric)}
          data={Object.entries(METRIC_LABELS).map(([value, label]) => ({
            value,
            label,
          }))}
        />
      </Group>
      <Chart option={option} label={`${METRIC_LABELS[metric]} p50, p95 and p99 over time, client side`} />
      <Text size="xs" c="dimmed">
        Client-side, per {series.bucket_seconds}-second bucket of send time. Bucket percentiles have no interval; the
        table below has the qualified values with 95% intervals.
      </Text>
    </Stack>
  );
}

function percentilesOption(series: ClientTimeseries, metric: Metric, palette: ChartPalette): ChartOption {
  const line = (name: "p50" | "p95" | "p99") => ({
    name,
    type: "line",
    showSymbol: false,
    connectNulls: false,
    lineStyle: { width: name === "p99" ? 2 : 1.5 },
    color: palette.percentiles[name],
    data: series.buckets.map((bucket) => [bucket.offset_seconds, bucket[metric]?.[name] ?? null]),
  });
  const lines = [line("p50"), line("p95"), line("p99")];
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 64, right: 16, top: STAGE_LABEL_SPACE + 24, bottom: 40 },
    legend: { top: 0, right: 0, textStyle: { color: palette.muted } },
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number | null) => formatSeconds(value),
    },
    xAxis: {
      type: "value",
      name: "seconds since the first stage",
      nameLocation: "middle",
      nameGap: 26,
      min: 0,
      max: series.stages.at(-1)?.end_offset_seconds,
      axisLabel: { color: palette.muted },
      splitLine: { show: false },
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
    series: [{ ...lines[0], markArea: stageBandsMarkArea(series.stages, palette) }, lines[1], lines[2]],
  };
}

function StagePercentiles({ analysis }: { analysis: AnalysisResult }) {
  const quality = new Map(analysis.quality.stages.map((stage) => [stage.stage_index, stage]));
  return (
    <Stack gap="xs">
      <Title order={4}>Per stage, with 95% intervals</Title>
      <Table.ScrollContainer minWidth={760}>
        <Table striped verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Stage</Table.Th>
              <Table.Th>Offered</Table.Th>
              <Table.Th>Validity</Table.Th>
              <Table.Th>TTFT p50</Table.Th>
              <Table.Th>TTFT p95</Table.Th>
              <Table.Th>TTFT p99</Table.Th>
              <Table.Th>E2E p95</Table.Th>
              <Table.Th>E2E p99</Table.Th>
              <Table.Th>ITL p95</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {analysis.stages.map((stage) => (
              <Table.Tr key={stage.stage_index}>
                <Table.Td>{stage.stage_index}</Table.Td>
                <Table.Td>{formatNumber(stage.offered_rps)} RPS</Table.Td>
                <Table.Td>
                  <StageValidity quality={quality.get(stage.stage_index)} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.ttft?.p50_seconds} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.ttft?.p95_seconds} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.ttft?.p99_seconds} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.e2e?.p95_seconds} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.e2e?.p99_seconds} />
                </Table.Td>
                <Table.Td>
                  <IntervalValue interval={stage.itl?.p95_seconds} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Stack>
  );
}

function LatencyVsRps({ analysis }: { analysis: AnalysisResult }) {
  const palette = useChartPalette();
  const option = useMemo(() => latencyVsRpsOption(analysis, palette), [analysis, palette]);
  return (
    <Stack gap="xs">
      <Title order={4}>p95 latency against achieved RPS</Title>
      <Chart
        option={option}
        label="p95 time to first token and end-to-end latency against achieved requests per second"
      />
      <Text size="xs" c="dimmed">
        {latencyVsRpsCaption(analysis)}
      </Text>
    </Stack>
  );
}

type HistogramKind = "ttft_counts" | "e2e_counts" | "itl_counts";
const HISTOGRAM_LABELS: Record<HistogramKind, string> = {
  ttft_counts: "Time to first token",
  e2e_counts: "End-to-end",
  itl_counts: "Inter-token",
};

function Histograms({ histograms }: { histograms: LatencyHistograms }) {
  const [stageIndex, setStageIndex] = useState(String(histograms.stages.at(-1)?.stage_index ?? 0));
  const [kind, setKind] = useState<HistogramKind>("e2e_counts");
  const palette = useChartPalette();
  const stage = histograms.stages.find((candidate) => String(candidate.stage_index) === stageIndex);
  const counts = stage?.[kind] ?? null;
  const option = useMemo(
    () => histogramOption(histograms.edges_seconds, counts, palette),
    [histograms, counts, palette],
  );
  return (
    <Stack gap="xs">
      <Group justify="space-between" align="end">
        <Title order={4}>Distribution</Title>
        <Group gap="xs">
          <SegmentedControl
            size="xs"
            value={kind}
            onChange={(value) => setKind(value as HistogramKind)}
            data={Object.entries(HISTOGRAM_LABELS).map(([value, label]) => ({
              value,
              label,
            }))}
          />
          <Select
            size="xs"
            w={120}
            aria-label="Stage"
            value={stageIndex}
            onChange={(value) => value && setStageIndex(value)}
            data={histograms.stages.map((candidate) => ({
              value: String(candidate.stage_index),
              label: `Stage ${candidate.stage_index}`,
            }))}
          />
        </Group>
      </Group>
      {counts === null ? (
        <Text size="sm" c="dimmed">
          This stage has no {HISTOGRAM_LABELS[kind].toLowerCase()} observations.
        </Text>
      ) : (
        <Chart option={option} label={`${HISTOGRAM_LABELS[kind]} histogram for stage ${stageIndex}`} />
      )}
    </Stack>
  );
}

/** Bars over the occupied range of the log buckets, labelled by their lower edge. */
function histogramOption(edges: number[], counts: number[] | null, palette: ChartPalette): ChartOption {
  const values = counts ?? [];
  const first = values.findIndex((count) => count > 0);
  const last = values.length - 1 - [...values].reverse().findIndex((count) => count > 0);
  const range = first < 0 ? [] : values.slice(first, last + 1).map((count, offset) => [first + offset, count] as const);
  const bucketLabel = (index: number) =>
    index === 0 ? `< ${formatSeconds(edges[0])}` : `≥ ${formatSeconds(edges[index - 1])}`;
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 56, right: 16, top: 16, bottom: 56 },
    tooltip: { trigger: "axis" },
    xAxis: {
      type: "category",
      data: range.map(([index]) => bucketLabel(index)),
      axisLabel: { color: palette.muted, rotate: 40, hideOverlap: true },
    },
    yAxis: {
      type: "value",
      name: "requests",
      axisLabel: { color: palette.muted },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    series: [
      {
        type: "bar",
        name: "count",
        color: palette.measure,
        barCategoryGap: "8%",
        data: range.map(([, count]) => count),
      },
    ],
  };
}
