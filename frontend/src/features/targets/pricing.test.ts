import { describe, expect, it } from "vitest";
import { pricingBody, pricingError, pricingFields } from "./pricing";

describe("a saved target's pricing fields", () => {
  it("round-trip a host and datasheet tokens, and name nothing when empty", () => {
    const pricing = {
      hourly: { kind: "host" as const, host_id: "01JHOST0000000000000000000" },
      token: { source: "datasheet" as const, url: null },
    };
    expect(pricingBody(pricingFields(pricing))).toEqual(pricing);
    expect(pricingBody(pricingFields(null))).toBeNull();
  });

  it("say what stops them saving", () => {
    const empty = pricingFields(null);
    expect(pricingError(empty)).toBeNull();
    expect(pricingError({ ...empty, hourly: "host" })).toMatch(/host/);
    expect(pricingError({ ...empty, hourly: "node_type", nodeType: "g6.xlarge", gpuCount: "0" })).toMatch(/GPUs/);
    expect(pricingError({ ...empty, token: "manual", inputPer1m: "0.2" })).toMatch(/both token prices/);
  });
});
