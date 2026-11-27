import { Stack, Table, Text, Title } from "@mantine/core";
import { useMemo } from "react";
import { useRunDocument } from "../../../api/hooks";
import type { AnalysisResult, ClientTimeseries, RunStatus } from "../../../api/types";
import { Chart } from "../../../charts/Chart";
import type { ChartPalette } from "../../../charts/palette";
import { STAGE_LABEL_SPACE, stageBandsMarkArea } from "../../../charts/stageBands";
import { useChartPalette, type ChartOption } from "../../../charts/useChart";
import { formatNumber, formatPercent, formatSeconds } from "../../../components/format";
import { StageValidity } from "../../../components/StageValidity";
import { WithDocument } from "./NotAnalysed";

export function ThroughputTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const analysis = useRunDocument("getAnalysis", runId, status);
  const timeseries = useRunDocument("getClientTimeseries", runId, status);
  return (
    <Stack gap="xl">
      <WithDocument query={timeseries} what="throughput over time">
        {(series) => <ThroughputCharts series={series} />}
      </WithDocument>
      <WithDocument query={analysis} what="stage statistics">
        {(document) => <StageThroughput analysis={document} />}
      </WithDocument>
    </Stack>
  );
}

function ThroughputCharts({ series }: { series: ClientTimeseries }) {
  const palette = useChartPalette();
  const rates = useMemo(() => ratesOption(series, palette), [series, palette]);
  const lag = useMemo(() => sendLagOption(series, palette), [series, palette]);
  const tokens = useMemo(() => tokensOption(series, palette), [series, palette]);
  const hasTokens = series.buckets.some((bucket) => bucket.output_tokens_per_second != null);
  return (
    <Stack gap="lg">
      <Stack gap="xs">
        <Title order={4}>Offered and achieved requests per second</Title>
        <Chart option={rates} label="Offered and achieved requests per second, and errors per second, over time" />
        <Text size="xs" c="dimmed">
          Achieved is requests sent per {series.bucket_seconds}-second bucket. Offered is each stage's rate. A stage
          counts only if achieved matched offered and send lag stayed low.
        </Text>
      </Stack>
      <Stack gap="xs">
        <Title order={4}>Send lag p99</Title>
        <Chart option={lag} label="Send lag p99 over time" height={200} />
        <Text size="xs" c="dimmed">
          How late each request left against its schedule. Lag that grows across a stage means the client, not the
          target, was the limit.
        </Text>
      </Stack>
      {hasTokens && (
        <Stack gap="xs">
          <Title order={4}>Output tokens per second</Title>
          <Chart option={tokens} label="Output tokens per second over time" height={200} />
        </Stack>
      )}
    </Stack>
  );
}

function timeAxes(series: ClientTimeseries, palette: ChartPalette, yName: string, format: (value: number) => string) {
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 64, right: 16, top: STAGE_LABEL_SPACE + 24, bottom: 40 },
    legend: { top: 0, right: 0, textStyle: { color: palette.muted } },
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
      name: yName,
      axisLabel: { color: palette.muted, formatter: format },
      splitLine: { lineStyle: { color: palette.grid } },
    },
  };
}

function ratesOption(series: ClientTimeseries, palette: ChartPalette): ChartOption {
  const offered = series.stages.flatMap((stage) => [
    [stage.start_offset_seconds, stage.offered_rps],
    [stage.end_offset_seconds, stage.offered_rps],
  ]);
  return {
    ...timeAxes(series, palette, "requests/s", (value) => formatNumber(value)),
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number) => `${formatNumber(value)} /s`,
    },
    series: [
      {
        name: "Achieved",
        type: "line",
        showSymbol: false,
        color: palette.measure,
        data: series.buckets.map((bucket) => [bucket.offset_seconds, bucket.achieved_rps]),
        markArea: stageBandsMarkArea(series.stages, palette),
      },
      {
        name: "Offered",
        type: "line",
        showSymbol: false,
        color: palette.offered,
        lineStyle: { type: "dashed" },
        data: offered,
      },
      {
        name: "Errors",
        type: "line",
        showSymbol: false,
        color: palette.invalid,
        data: series.buckets.map((bucket) => [bucket.offset_seconds, bucket.errors / series.bucket_seconds]),
      },
    ],
  };
}

function sendLagOption(series: ClientTimeseries, palette: ChartPalette): ChartOption {
  return {
    ...timeAxes(series, palette, "seconds", (value) => formatSeconds(value)),
    legend: undefined,
    grid: { left: 64, right: 16, top: STAGE_LABEL_SPACE, bottom: 40 },
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number | null) => formatSeconds(value),
    },
    series: [
      {
        name: "Send lag p99",
        type: "line",
        showSymbol: false,
        color: palette.warning,
        data: series.buckets.map((bucket) => [bucket.offset_seconds, bucket.send_lag_p99_seconds ?? null]),
        markArea: stageBandsMarkArea(series.stages, palette),
      },
    ],
  };
}

function tokensOption(series: ClientTimeseries, palette: ChartPalette): ChartOption {
  return {
    ...timeAxes(series, palette, "tokens/s", (value) => formatNumber(value)),
    legend: undefined,
    grid: { left: 64, right: 16, top: STAGE_LABEL_SPACE, bottom: 40 },
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number | null) => `${formatNumber(value)} tokens/s`,
    },
    series: [
      {
        name: "Output tokens/s",
        type: "line",
        showSymbol: false,
        color: palette.measure,
        data: series.buckets.map((bucket) => [bucket.offset_seconds, bucket.output_tokens_per_second ?? null]),
        markArea: stageBandsMarkArea(series.stages, palette),
      },
    ],
  };
}

function StageThroughput({ analysis }: { analysis: AnalysisResult }) {
  const quality = new Map(analysis.quality.stages.map((stage) => [stage.stage_index, stage]));
  return (
    <Stack gap="xs">
      <Title order={4}>Per stage</Title>
      <Table.ScrollContainer minWidth={760}>
        <Table striped verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Stage</Table.Th>
              <Table.Th>Validity</Table.Th>
              <Table.Th>Offered</Table.Th>
              <Table.Th>Achieved</Table.Th>
              <Table.Th>Goodput</Table.Th>
              <Table.Th>Requests</Table.Th>
              <Table.Th>Errors</Table.Th>
              <Table.Th>Send lag p99</Table.Th>
              <Table.Th>Output tokens/s</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {analysis.stages.map((stage) => (
              <Table.Tr key={stage.stage_index}>
                <Table.Td>{stage.stage_index}</Table.Td>
                <Table.Td>
                  <StageValidity quality={quality.get(stage.stage_index)} />
                </Table.Td>
                <Table.Td>{formatNumber(stage.offered_rps)} RPS</Table.Td>
                <Table.Td>{formatNumber(stage.achieved_rps)} RPS</Table.Td>
                <Table.Td>{stage.goodput_rps == null ? "–" : `${formatNumber(stage.goodput_rps)} RPS`}</Table.Td>
                <Table.Td>{stage.requests.toLocaleString("en-US")}</Table.Td>
                <Table.Td>
                  {stage.errors.toLocaleString("en-US")}
                  {stage.requests > 0 && (
                    <Text span size="xs" c="dimmed">
                      {" "}
                      ({formatPercent(stage.errors / stage.requests)})
                    </Text>
                  )}
                  {(stage.errors_by_class ?? []).length > 0 && (
                    <Text size="xs" c="dimmed">
                      {(stage.errors_by_class ?? []).map((entry) => `${entry.error_class} ${entry.count}`).join(", ")}
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>{formatSeconds(stage.send_lag_p99_seconds)}</Table.Td>
                <Table.Td>{formatNumber(stage.output_tokens_per_second)}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Stack>
  );
}
