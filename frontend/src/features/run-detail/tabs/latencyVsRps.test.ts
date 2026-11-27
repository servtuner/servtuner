import { describe, expect, it } from "vitest";
import type { AnalysisResult } from "../../../api/types";
import { lightPalette } from "../../../charts/palette";
import { goldenAnalysis } from "../../../test/bundles";
import { latencyVsRpsOption } from "./latencyVsRps";

type Series = {
  name: string;
  type: string;
  data: { value: [number, number | null]; stage: number }[];
};

// The RTX 3070 baseline of 2026-11-24: stages 3 to 5 offered 16, 24 and 32 RPS, achieved 15, 10
// and 10, and were excluded as saturated. Joined in stage order, the line ran backwards.
function saturated(): AnalysisResult {
  const analysis: AnalysisResult = structuredClone(goldenAnalysis);
  const template = analysis.stages[0];
  const quality = analysis.quality.stages[0];
  if (!template || !quality) throw new Error("the golden analysis has stages");
  const ladder: [number, number][] = [
    [4, 3.63],
    [8, 8.06],
    [12, 12.5],
    [16, 15.13],
    [24, 10.22],
    [32, 10.13],
  ];
  analysis.stages = ladder.map(([offered, achieved], index) => ({
    ...template,
    stage_index: index,
    offered_rps: offered,
    achieved_rps: achieved,
  }));
  analysis.quality.stages = ladder.map((_, index) => ({
    ...quality,
    stage_index: index,
    is_excluded: index >= 3,
  }));
  return analysis;
}

describe("p95 latency against achieved RPS", () => {
  it("joins only included stages, and draws excluded ones as separate points", () => {
    const series = latencyVsRpsOption(saturated(), lightPalette).series as Series[];
    const lines = series.filter((one) => one.type === "line");
    const points = series.filter((one) => one.type === "scatter");
    for (const line of lines) {
      expect(line.data.map((point) => point.stage)).toEqual([0, 1, 2]);
      const xs = line.data.map((point) => point.value[0]);
      expect(xs).toEqual([...xs].sort((a, b) => a - b)); // never backwards
    }
    expect(points.map((one) => one.name)).toEqual(["TTFT p95", "E2E p95"]);
    expect(points[0]?.data.map((point) => point.stage)).toEqual([3, 4, 5]);
  });

  it("has no extra series when nothing is excluded", () => {
    const series = latencyVsRpsOption(goldenAnalysis, lightPalette).series as Series[];
    expect(
      series.every((one) => one.type === "line") || goldenAnalysis.quality.stages.some((stage) => stage.is_excluded),
    ).toBe(true);
  });
});
