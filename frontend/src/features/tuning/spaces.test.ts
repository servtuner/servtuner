import { parse } from "yaml";
import { describe, expect, it } from "vitest";
import type { ManagedTarget } from "../../api/types";
import { searchSpaces } from "../../test/bundles";
import { managedTarget } from "../../test/fixtures";
import { fitsTarget, hintProbeRates, measuredProbeRates, rankForTarget, templateText, withProbeRates } from "./spaces";

const [oneGpu, hostOnly] = searchSpaces as [(typeof searchSpaces)[0], (typeof searchSpaces)[0]];
const withGpus = (gpus: number) => ({ ...managedTarget, spec: { ...managedTarget.spec, gpus } }) as ManagedTarget;
const host = { ...managedTarget, spec: { placement: "host" } } as ManagedTarget;

describe("starting search spaces", () => {
  it("fit by placement, and by a Kubernetes target's GPU count", () => {
    expect(fitsTarget(oneGpu, withGpus(1))).toBe(true);
    expect(fitsTarget(oneGpu, withGpus(2))).toBe(false);
    expect(fitsTarget(oneGpu, host)).toBe(true); // a host declares no GPU count
    expect(fitsTarget(hostOnly, withGpus(1))).toBe(false);
    expect(fitsTarget(hostOnly, host)).toBe(true);
  });

  it("fit by server: an SGLang space suits an SGLang target only, and a space without servers is vLLM's", () => {
    const sglangSpace = { ...oneGpu, fits: { ...oneGpu.fits, servers: ["sglang" as const] } };
    const sglangTarget = { ...withGpus(1), spec: { ...withGpus(1).spec, server: "sglang" } } as ManagedTarget;
    expect(fitsTarget(sglangSpace, sglangTarget)).toBe(true);
    expect(fitsTarget(sglangSpace, withGpus(1))).toBe(false);
    expect(fitsTarget(oneGpu, sglangTarget)).toBe(false);
  });

  it("list the fitting ones first", () => {
    expect(rankForTarget(searchSpaces, host)).toEqual({ fitting: [oneGpu, hostOnly], others: [] });
    expect(rankForTarget(searchSpaces, withGpus(1))).toEqual({ fitting: [oneGpu], others: [hostOnly] });
    expect(rankForTarget(searchSpaces, undefined)).toEqual({ fitting: [], others: searchSpaces });
  });

  it("become YAML that says where it came from and parses back to the space", () => {
    const text = templateText(oneGpu);
    expect(text.startsWith("# Starting space single-gpu@1: One GPU.")).toBe(true);
    expect(parse(text)).toEqual(oneGpu.space);
  });

  it("probe around a measured SLO-max, or double from a hand-written hint", () => {
    expect(measuredProbeRates(20)).toEqual([10, 15, 20, 25]);
    expect(measuredProbeRates(0.1)).toEqual([0.1, 0.1, 0.1, 0.1]);
    expect(hintProbeRates(8)).toEqual([8, 16, 32, 64]);
  });

  it("set ladder.probe_rps in the space, keeping its comments and edits", () => {
    const text = "# mine\nmin_concurrency: 4\nladder:\n  full_factors: [1]\n";
    const updated = withProbeRates(text, [2, 4]);
    expect(updated).toContain("# mine");
    expect(parse(updated)).toEqual({ min_concurrency: 4, ladder: { full_factors: [1], probe_rps: [2, 4] } });
    expect(withProbeRates("knobs: [", [2])).toBe("knobs: ["); // unparsable: left for the user to fix
  });
});
