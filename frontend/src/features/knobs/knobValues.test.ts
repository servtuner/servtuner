import { describe, expect, it } from "vitest";
import { knobCatalog } from "../../test/fixtures";
import { describeLimits, displayValue, initialValue, knobsOf, matchesSearch, problemOf } from "./knobValues";

const catalog = new Map(knobCatalog.knobs.map((knob) => [knob.name, knob]));
const knob = (name: string) => catalog.get(name)!;

describe("knob values", () => {
  it("checks a value against the limits in vLLM's source", () => {
    const gpu = knob("vllm.gpu_memory_utilization");
    expect(problemOf({ name: gpu.name, value: 0.9 }, gpu)).toBeNull();
    expect(problemOf({ name: gpu.name, value: 0 }, gpu)).toBe("More than 0");
    expect(problemOf({ name: gpu.name, value: 1.2 }, gpu)).toBe("At most 1");
    expect(problemOf({ name: "vllm.max_num_seqs", value: 0 }, knob("vllm.max_num_seqs"))).toBe("At least 1");
    expect(problemOf({ name: "vllm.max_num_seqs", value: 1.5 }, knob("vllm.max_num_seqs"))).toBe("A whole number");
    expect(problemOf({ name: "vllm.dtype", value: "fp4" }, knob("vllm.dtype"))).toBe("One of auto, bfloat16, float16");
    expect(problemOf({ name: "vllm.max_num_seqs", value: null }, knob("vllm.max_num_seqs"))).toBe("Set a value");
    expect(describeLimits(gpu)).toBe("more than 0 and at most 1");
  });

  it("never invents a starting number vLLM does not give", () => {
    expect(initialValue(knob("vllm.max_num_seqs"))).toBeNull(); // vLLM decides at start-up
    expect(initialValue(knob("vllm.dtype"))).toBe("auto");
    expect(initialValue(knob("vllm.enable_prefix_caching"))).toBe(true);
  });

  it("deploys only when every knob has a good value", () => {
    expect(knobsOf([{ name: "vllm.max_num_seqs", value: 128 }], catalog)).toEqual({ "vllm.max_num_seqs": 128 });
    expect(knobsOf([{ name: "vllm.max_num_seqs", value: null }], catalog)).toBeNull();
    expect(knobsOf([{ name: "vllm.max_num_seqs", value: 0 }], catalog)).toBeNull();
    expect(knobsOf([], catalog)).toEqual({});
  });

  it("finds a knob by its label, flag or name", () => {
    const seqs = knob("vllm.max_num_seqs");
    expect(matchesSearch(seqs, "max seq")).toBe(true);
    expect(matchesSearch(seqs, "--max-num-seqs")).toBe(true);
    expect(matchesSearch(seqs, "vllm.max_num_seqs")).toBe(true);
    expect(matchesSearch(seqs, "gpu")).toBe(false);
    expect(displayValue({ name: "vllm.enable_prefix_caching", value: false }, knob("vllm.enable_prefix_caching"))).toBe(
      "off",
    );
  });
});
