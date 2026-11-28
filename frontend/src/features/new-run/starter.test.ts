import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import { managedTarget } from "../../test/fixtures";
import { STARTER_SPEC, starterForManagedTarget, withRunDefaults } from "./starter";

describe("New run for a managed server", () => {
  it("points the template at the target and declares its deployed knobs", () => {
    const spec = parse(
      starterForManagedTarget({
        ...managedTarget,
        deployment: { knobs: { "vllm.max_num_seqs": 64 }, args: [], deployed_at: "2026-11-24T09:00:00Z" },
      }),
    );
    expect(spec.name).toBe("qwen-bench");
    expect(spec.target).toEqual(managedTarget.run_target);
    expect(spec.knobs).toEqual({ "vllm.max_num_seqs": 64 });
    expect(spec.labels).toEqual(managedTarget.run_labels);
    // Everything else is the template's, so the run is valid as bench's test of it says.
    expect(spec.load).toEqual(parse(STARTER_SPEC).load);
    expect(spec.workload).toEqual(parse(STARTER_SPEC).workload);
  });

  it("prices the run by its host when the host is priced", () => {
    const infraPricing = {
      source: "static",
      static: { gpus: { "NVIDIA GeForce RTX 3070": { usd_per_hour: 0.08 } } },
    };
    const spec = parse(starterForManagedTarget({ ...managedTarget, run_analysis: { infra_pricing: infraPricing } }));
    expect(spec.analysis.infra_pricing).toEqual(infraPricing);
    expect(parse(starterForManagedTarget(managedTarget)).analysis?.infra_pricing).toBeUndefined();
  });

  it("declares no knobs before the first deploy", () => {
    expect(parse(starterForManagedTarget(managedTarget))).not.toHaveProperty("knobs");
  });

  it("loads the target around its measured SLO-max", () => {
    const spec = parse(starterForManagedTarget(managedTarget, 12));
    expect(spec.load.stages).toEqual([
      { rate: 8.4, duration: "60s" },
      { rate: 12, duration: "60s" },
      { rate: 15.6, duration: "60s" },
    ]);
    expect(spec.load.warmup).toBe(parse(STARTER_SPEC).load.warmup);
  });

  it("names the install's Prometheus when the spec names none", () => {
    const prometheus = { url: "http://kps-prometheus.monitoring.svc:9090", tls: { enabled: false as const } };
    expect(parse(withRunDefaults(STARTER_SPEC, { prometheus })).prometheus).toEqual(prometheus);
    expect(withRunDefaults(STARTER_SPEC, {})).toBe(STARTER_SPEC);
    expect(withRunDefaults(STARTER_SPEC, undefined)).toBe(STARTER_SPEC);
    const own = `${STARTER_SPEC}prometheus:\n  url: http://mine:9090\n  tls: {enabled: false}\n`;
    expect(parse(withRunDefaults(own, { prometheus })).prometheus.url).toBe("http://mine:9090");
  });

  it("prices the run from the install's price book unless it sets its own", () => {
    const infra_pricing = { source: "aws", aws: { region: "eu-central-1" } };
    expect(parse(withRunDefaults(STARTER_SPEC, { infra_pricing })).analysis.infra_pricing).toEqual(infra_pricing);
    const own = `${STARTER_SPEC}analysis:\n  target_rps: 5\n  infra_pricing: {source: none}\n`;
    const kept = parse(withRunDefaults(own, { infra_pricing })).analysis;
    expect(kept).toEqual({ target_rps: 5, infra_pricing: { source: "none" } });
  });
});
