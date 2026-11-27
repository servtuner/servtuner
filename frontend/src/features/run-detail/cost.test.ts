import { describe, expect, it } from "vitest";
import type { CapacityTable } from "../../api/types";
import { costLines } from "./cost";

const priced = {
  analyzer_version: "test",
  rows: [],
  caveats: [],
  reason: "test",
  infra_cost: {
    source: "static",
    is_priced: true,
    usd_per_replica_hour: 0.05,
    basis: "1 GPU × 0.05 USD/GPU-h",
    usd_per_1k_requests: 0.00278,
    usd_per_1m_output_tokens: 0.0278,
  },
} as CapacityTable;

describe("a run's cost", () => {
  it("shows the replica-hour with its basis and the cost per request at the SLO-max", () => {
    const lines = costLines(priced);
    expect(lines[0]).toEqual({ text: "$0.05 per replica-hour (static prices)", hover: "1 GPU × 0.05 USD/GPU-h" });
    expect(lines[1]?.text).toBe(
      "Infrastructure at the SLO-max: $0.00278 per 1k requests, $0.0278 per 1M output tokens",
    );
  });

  it("says why a priced replica has no cost per request, and never shows $0", () => {
    const infra = { source: "static" as const, is_priced: true, usd_per_replica_hour: 0.05 };
    const lines = costLines({ ...priced, infra_cost: { ...infra, unit_cost_reason: "no SLO-max" } });
    expect(lines.map((line) => line.text)).toEqual([
      "$0.05 per replica-hour (static prices)",
      "No cost per request: no SLO-max",
    ]);
    expect(costLines({ ...priced, infra_cost: undefined })[0]?.text).toMatch(/^Not priced/);
  });

  it("dates a datasheet's prices and says when they came from the price book", () => {
    const token = {
      model: "gpt-4o-mini",
      is_priced: true,
      method: "datasheet" as const,
      usd_per_1k_requests: 0.12,
      usd_per_1m_output_tokens: 0.9,
      datasheet_fetched_at: "2026-11-26T14:09:41Z",
      datasheet_source: "price_book" as const,
    };
    const [line] = costLines({ ...priced, infra_cost: undefined, token_cost: token });
    expect(line?.text).toBe(
      "Tokens: $0.12 per 1k requests, $0.9 per 1M output tokens (prices as of 2026-11-26, from the price book)",
    );
    const manual = costLines({ ...priced, infra_cost: undefined, token_cost: { ...token, method: "manual" } })[0];
    expect(manual?.hover).toBe("Tokens × the prices given for this target");
    expect(manual?.text).not.toMatch(/as of/);
  });
});
