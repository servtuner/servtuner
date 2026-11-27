// The p95 latency against achieved RPS chart of the Latency tab (run detail).
import type { AnalysisResult } from "../../../api/types";
import type { ChartPalette } from "../../../charts/palette";
import type { ChartOption } from "../../../charts/useChart";
import { formatNumber, formatSeconds } from "../../../components/format";

function ttftSloOf(analysis: AnalysisResult): number | undefined {
  return analysis.stages.flatMap((stage) => stage.slo_results ?? []).find((result) => result.slo === "ttft_p95")
    ?.threshold;
}

export function latencyVsRpsCaption(analysis: AnalysisResult): string {
  const parts = ["One point per stage."];
  if (analysis.quality.stages.some((stage) => stage.is_excluded))
    parts.push(
      "Hollow points are excluded stages, left off the line: a saturated target completes fewer requests than offered, so they can sit left of lower stages.",
    );
  if (ttftSloOf(analysis) != null) parts.push("The dashed line is the TTFT p95 SLO.");
  if (analysis.capacity.knee) parts.push(`The diamond marks the knee, at stage ${analysis.capacity.knee.stage_index}.`);
  return parts.join(" ");
}

export function latencyVsRpsOption(analysis: AnalysisResult, palette: ChartPalette): ChartOption {
  const excluded = new Set(
    analysis.quality.stages.filter((stage) => stage.is_excluded).map((stage) => stage.stage_index),
  );
  const knee = analysis.capacity.knee?.stage_index;
  type Stage = AnalysisResult["stages"][number];
  const point = (stage: Stage, value: number | null | undefined) => ({
    value: [stage.achieved_rps, value ?? null],
    offered: stage.offered_rps,
    stage: stage.stage_index,
    symbol: excluded.has(stage.stage_index) ? "emptyCircle" : stage.stage_index === knee ? "diamond" : "circle",
    symbolSize: stage.stage_index === knee ? 12 : 7,
  });
  // The line joins the included stages only. An excluded stage is usually saturated: it achieved
  // less than it was offered, so joined in stage order the line would run backwards.
  const included = analysis.stages.filter((stage) => !excluded.has(stage.stage_index));
  const dropped = analysis.stages.filter((stage) => excluded.has(stage.stage_index));
  const ttftSlo = ttftSloOf(analysis);
  return {
    animation: false,
    textStyle: { color: palette.ink },
    grid: { left: 64, right: 16, top: 36, bottom: 40 },
    legend: { top: 0, right: 0, textStyle: { color: palette.muted } },
    tooltip: {
      trigger: "item",
      formatter: (params: {
        seriesName: string;
        data: {
          value: [number, number | null];
          offered: number;
          stage: number;
        };
      }) => {
        const { value, offered, stage } = params.data;
        const latency = value[1] == null ? "n/a" : formatSeconds(value[1]);
        return `${params.seriesName}, stage ${stage}: ${latency}<br/>achieved ${formatNumber(value[0])} of ${formatNumber(offered)} RPS offered`;
      },
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
    series: [
      {
        name: "TTFT p95",
        type: "line",
        color: palette.measure,
        data: included.map((stage) => point(stage, stage.ttft?.p95_seconds?.value)),
        markLine:
          ttftSlo == null
            ? undefined
            : {
                silent: true,
                symbol: "none",
                lineStyle: { type: "dashed", color: palette.warning },
                label: {
                  formatter: `SLO ${formatSeconds(ttftSlo)}`,
                  color: palette.warning,
                },
                data: [{ yAxis: ttftSlo }],
              },
      },
      {
        name: "E2E p95",
        type: "line",
        color: palette.offered,
        data: included.map((stage) => point(stage, stage.e2e?.p95_seconds?.value)),
      },
      // Same names as the lines: same colours, and the legend shows and hides them together.
      ...(dropped.length === 0
        ? []
        : [
            {
              name: "TTFT p95",
              type: "scatter",
              color: palette.measure,
              data: dropped.map((stage) => point(stage, stage.ttft?.p95_seconds?.value)),
            },
            {
              name: "E2E p95",
              type: "scatter",
              color: palette.offered,
              data: dropped.map((stage) => point(stage, stage.e2e?.p95_seconds?.value)),
            },
          ]),
    ],
  };
}
