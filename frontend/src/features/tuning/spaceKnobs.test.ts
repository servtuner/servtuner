import { parse } from "yaml";
import { describe, expect, it } from "vitest";
import { knobCatalog } from "../../test/fixtures";
import { describeTuned, newTunedKnob, tunedKnobsOf, tunedProblem, withBase, withTunedKnobs } from "./spaceKnobs";

const byName = new Map(knobCatalog.knobs.map((knob) => [knob.name, knob]));
const seqs = byName.get("vllm.max_num_seqs");
const caching = byName.get("vllm.enable_prefix_caching")!;

const SPACE = `# Starting space single-gpu@1
min_concurrency: 16 # mine
knobs:
  - {name: vllm.max_num_seqs, tier: scheduler, min: 16, max: 512, default: 128}
ladder: {probe_rps: [1, 2]}
`;

describe("a space's knobs", () => {
  it("are rewritten one flow map each, keeping the rest and its comments", () => {
    const knobs = tunedKnobsOf(parse(SPACE));
    const updated = withTunedKnobs(SPACE, [
      ...knobs,
      { name: "vllm.enable_prefix_caching", tier: "fine_tuning", values: [false, true] },
    ]);
    expect(updated).toContain("# mine");
    expect(updated).toContain("  - {name: vllm.max_num_seqs, tier: scheduler, min: 16, max: 512, default: 128}\n");
    expect(updated).toContain("  - {name: vllm.enable_prefix_caching, tier: fine_tuning, values: [false, true]}\n");
    expect(parse(updated).ladder).toEqual({ probe_rps: [1, 2] });
    expect(withTunedKnobs("knobs: [", [])).toBe("knobs: ["); // unparsable: left for the user to fix
  });

  it("set fixed knobs under base, and drop base when none are left", () => {
    const fixed = withBase(SPACE, { "vllm.dtype": "bfloat16" });
    expect(parse(fixed).base).toEqual({ "vllm.dtype": "bfloat16" });
    expect(parse(withBase(fixed, {}))).not.toHaveProperty("base");
  });

  it("are checked against vLLM's limits, as values or as a range", () => {
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "scheduler", min: 16, max: 512 }, seqs)).toBeNull();
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "scheduler", min: 512, max: 16 }, seqs)).toMatch(/above/);
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "scheduler", min: 0, max: 16 }, seqs)).toBe("At least 1");
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "scheduler" }, seqs)).toBe("Set both bounds");
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "fine_tuning", values: [] }, seqs)).toMatch(/one value/);
    expect(tunedProblem({ name: "vllm.max_num_seqs", tier: "fine_tuning", values: [64, 0.5] }, seqs)).toBe(
      "A whole number",
    );
  });

  it("read as a chip's value", () => {
    expect(describeTuned({ name: "vllm.max_num_seqs", tier: "scheduler", min: 16, max: 512 }, seqs)).toBe("16–512");
    expect(describeTuned(newTunedKnob(caching), caching)).toBe("off or on");
  });
});
