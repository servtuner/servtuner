import { describe, expect, it } from "vitest";
import { knobCatalog } from "../../test/fixtures";
import { flagOf, overriddenLine, unknownFlags } from "./baseFlags";

const catalog = knobCatalog;
const seqs = catalog.knobs.find((knob) => knob.name === "vllm.max_num_seqs")!;

describe("base flags", () => {
  it("are read as the flag they set", () => {
    expect(flagOf("--max-num-seqs=64")).toBe("--max-num-seqs");
    expect(flagOf("  --tool-call-parser hermes")).toBe("--tool-call-parser");
    expect(flagOf("-cc.cudagraph_mode=FULL")).toBe("-cc");
    expect(flagOf("hermes")).toBeNull();
  });

  it("warn about a flag vLLM does not take, never one it does", () => {
    expect(
      unknownFlags(
        ["--max-num-seqs=64", "--disable-prefix-caching", "-cc.cudagraph_mode=FULL", "-O3", "--max-num-sqs=8"],
        catalog,
      ),
    ).toEqual(["--max-num-sqs"]);
  });

  it("say which line a knob overrides", () => {
    expect(overriddenLine(["--dtype=half", "--max-num-seqs 32"], seqs)).toBe("--max-num-seqs 32");
    expect(overriddenLine(["--dtype=half"], seqs)).toBeNull();
  });
});
