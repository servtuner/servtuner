import { SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useMemo } from "react";
import { useRunDocument } from "../../../api/hooks";
import type { AnalysisResult, ClientTimeseries, RunStatus, ServerChart } from "../../../api/types";
import { Chart } from "../../../charts/Chart";
import type { ChartPalette } from "../../../charts/palette";
import { STAGE_LABEL_SPACE, stageBandsMarkArea } from "../../../charts/stageBands";
import { useChartPalette, type ChartOption } from "../../../charts/useChart";
import {
  formatBytes,
  formatCores,
  formatHertz,
  formatNumber,
  formatPercent,
  formatSeconds,
} from "../../../components/format";
import { WithDocument } from "./NotAnalysed";

type Panel = ServerChart["panels"][number];
type Group = Panel["group"];

// The panel that shows each knee bottleneck, so the cause is drawn first.
const BOTTLENECK_PANEL: Record<string, string> = {
  cpu: "target_cpu_usage",
  kv_cache: "kv_cache_usage",
  router_concurrency: "router_concurrency",
  gpu_engine_activity: "gpu_engine_active",
  gpu_util: "gpu_util",
};

const EMPTY: Record<Group, string> = {
  server: "No server-side series were collected for this target: no Prometheus, or its catalog returned nothing.",
  resources: "No CPU or memory series: the target is outside Kubernetes, or the kubernetes catalog was not collected.",
};

/** The Server and Resources tabs: one chart per collected series, with its definition beside it. */
export function ServerPanels({ runId, status, group }: { runId: string; status: RunStatus | undefined; group: Group }) {
  const chart = useRunDocument("getServerChart", runId, status);
  const timeseries = useRunDocument("getClientTimeseries", runId, status);
  const analysis = useRunDocument("getAnalysis", runId, status);
  return (
    <WithDocument query={chart} what={group === "server" ? "server series" : "resource series"}>
      {(document) => {
        const panels = ordered(
          document.panels.filter((panel) => panel.group === group),
          analysis.data ?? undefined,
        );
        if (panels.length === 0) return <Text c="dimmed">{EMPTY[group]}</Text>;
        return (
          <SimpleGrid cols={{ base: 1, lg: 2 }} spacing="xl">
            {panels.map((panel) => (
              <PanelChart key={panel.id} panel={panel} stages={timeseries.data?.stages ?? []} />
            ))}
          </SimpleGrid>
        );
      }}
    </WithDocument>
  );
}

function ordered(panels: Panel[], analysis: AnalysisResult | undefined): Panel[] {
  const first = BOTTLENECK_PANEL[analysis?.capacity.knee?.bottleneck?.resource ?? ""];
  return [...panels].sort((a, b) => Number(b.id === first) - Number(a.id === first));
}

function PanelChart({ panel, stages }: { panel: Panel; stages: ClientTimeseries["stages"] }) {
  const palette = useChartPalette();
  const option = useMemo(() => panelOption(panel, stages, palette), [panel, stages, palette]);
  return (
    <Stack gap={4}>
      <Title order={5}>{panel.title}</Title>
      <Chart option={option} label={panel.title} height={220} />
      {panel.definition && (
        <Text size="xs" c="dimmed">
          {panel.definition}
        </Text>
      )}
    </Stack>
  );
}

function formatter(unit: Panel["unit"]): (value: number) => string {
  switch (unit) {
    case "fraction":
      return (value) => formatPercent(value);
    case "bytes":
      return (value) => formatBytes(value);
    case "cores":
      return (value) => formatCores(value);
    case "seconds":
      return (value) => formatSeconds(value);
    case "per_second":
      return (value) => `${formatNumber(value, 3)} /s`;
    case "tokens_per_second":
      return (value) => `${formatNumber(value, 3)} tokens/s`;
    case "watts":
      return (value) => `${formatNumber(value, 3)} W`;
    case "hertz":
      return (value) => formatHertz(value);
    case "requests":
    case "pods":
      return (value) => formatNumber(value);
    default: {
      const unhandled: never = unit;
      throw new Error(`no formatter for panel unit ${String(unhandled)}`);
    }
  }
}

function panelOption(panel: Panel, stages: ClientTimeseries["stages"], palette: ChartPalette): ChartOption {
  const format = formatter(panel.unit);
  const lines = (panel.reference_lines ?? []).map((line) => ({
    yAxis: line.value,
    name: `${line.name} ${format(line.value)}`,
  }));
  const series = panel.series[0]!;
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: {
      left: 72,
      right: 16,
      top: stages.length ? STAGE_LABEL_SPACE : 16,
      bottom: 32,
    },
    tooltip: {
      trigger: "axis",
      valueFormatter: (value: number) => format(value),
    },
    xAxis: {
      type: "value",
      min: 0,
      max: stages.at(-1)?.end_offset_seconds,
      axisLabel: { color: palette.muted },
      splitLine: { show: false },
    },
    yAxis: {
      type: "value",
      min: 0,
      // Reference lines (a limit far above the usage) stay on the chart: the gap is the point.
      max: (value: { max: number }) =>
        Math.max(
          value.max,
          panel.unit === "fraction" ? 1 : 0,
          ...(panel.reference_lines ?? []).map((line) => line.value * 1.05),
        ),
      axisLabel: { color: palette.muted, formatter: format },
      splitLine: { lineStyle: { color: palette.grid } },
    },
    series: [
      {
        name: "range",
        type: "line",
        showSymbol: false,
        lineStyle: { opacity: 0 },
        areaStyle: { color: palette.band, opacity: 1 },
        data: series.points.map((point) => [point.offset_seconds, point.max]),
        markArea: stages.length ? stageBandsMarkArea(stages, palette) : undefined,
      },
      {
        name: series.name,
        type: "line",
        showSymbol: false,
        color: palette.measure,
        data: series.points.map((point) => [point.offset_seconds, point.mean]),
        markLine: lines.length
          ? {
              silent: true,
              symbol: "none",
              lineStyle: { type: "dashed", color: palette.warning },
              label: {
                position: "insideEndTop",
                color: palette.warning,
                formatter: (params: { name: string }) => params.name,
              },
              data: lines,
            }
          : undefined,
      },
    ],
  };
}
