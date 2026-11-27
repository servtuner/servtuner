import { describe, expect, it } from "vitest";
import { workloadPresets } from "../../test/fixtures";
import { customizeChanges, presetChanges, startingLadder } from "./presets";

const rag = workloadPresets[1]!;

describe("preset changes", () => {
  it("names the preset, drops the explicit workload, keeps streaming and fills the SLOs", () => {
    const changes = new Map(
      presetChanges(rag, { streaming: false }, "loadgen").map(([path, value]) => [path.join("."), value]),
    );
    expect(changes.get("workload.preset")).toBe("rag-prefill@1");
    expect(changes.has("workload.dataset") && changes.get("workload.dataset")).toBeUndefined();
    expect(changes.get("workload.streaming")).toBe(false);
    expect(changes.get("slos.ttft_p95")).toBe("2s");
    expect(changes.has("engine.type")).toBe(false);
  });

  it("swaps an engine the preset cannot run on for its first", () => {
    const changes = presetChanges(rag, undefined, "inference-perf");
    expect(changes).toContainEqual([["engine", "type"], "loadgen"]);
    expect(changes).toContainEqual([["workload", "streaming"], true]);
  });

  it("customizing writes the preset's workload out and removes its name", () => {
    const changes = new Map(customizeChanges(rag).map(([path, value]) => [path.join("."), value]));
    expect(changes.get("workload.preset")).toBeUndefined();
    expect(changes.get("workload.api")).toBe("chat");
    expect(changes.get("workload.dataset")).toEqual(rag.workload.dataset);
  });

  it("builds a doubling ladder from the hint", () => {
    expect(startingLadder(0.5)).toEqual([
      { rate: 0.5, duration: "90s" },
      { rate: 1, duration: "90s" },
      { rate: 2, duration: "90s" },
      { rate: 4, duration: "90s" },
    ]);
  });
});
