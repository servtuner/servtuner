import { parseDocument } from "yaml";
import type { ManagedTarget, RunDefaults } from "../../api/types";

// The template a new run starts from: the mock target on kind, which is valid
// as it is (a test checks bench accepts it), so Validate passes before any edit.
export const STARTER_SPEC = `apiVersion: servtuner.dev/v2
kind: RunSpec
name: my-run
# experiment: my-experiment   # group runs to compare them
target:
  type: openai-compatible
  endpoint: http://openai-mock.servtuner-mock.svc:8000/v1
  model: mock-model
  tls:
    enabled: false
engine:
  type: loadgen
workload:
  api: chat
  streaming: true
  dataset:
    type: synthetic
    input_tokens:
      mean: 256
    output_tokens:
      mean: 128
load:
  pacing: poisson
  warmup: 30s
  stages:
    - rate: 10
      duration: 60s
    - rate: 20
      duration: 60s
slos:
  ttft_p95: 500ms
`;

// Benchmark's stages around a target's measured SLO-max, as a tuning session screens a
// configuration (servtuner propose's screen_factors): below, at and above the knee.
const CAPACITY_FACTORS = [0.7, 1, 1.3];
const STAGE_DURATION = "60s";

/**
 * The template pointed at a managed vLLM: its run target and run labels (as the tuning loop's
 * runs have them), the last deployment's knobs declared so bench checks them against what vLLM
 * really got, its host's prices (run_analysis), and, when an earlier run measured it,
 * stages around its SLO-max.
 */
export function starterForManagedTarget(target: ManagedTarget, sloMaxRps?: number): string {
  const document = parseDocument(STARTER_SPEC);
  document.setIn(["name"], `${target.name}-bench`);
  document.setIn(["target"], document.createNode(target.run_target));
  document.setIn(["labels"], document.createNode(target.run_labels)); // its page lists runs by them
  if (target.run_analysis?.infra_pricing)
    document.setIn(["analysis", "infra_pricing"], document.createNode(target.run_analysis.infra_pricing));
  const knobs = target.deployment?.knobs;
  if (knobs && Object.keys(knobs).length > 0) document.setIn(["knobs"], document.createNode(knobs));
  if (sloMaxRps && sloMaxRps > 0) {
    const stages = CAPACITY_FACTORS.map((factor) => ({
      rate: Math.max(0.1, Math.round(sloMaxRps * factor * 10) / 10),
      duration: STAGE_DURATION,
    }));
    document.setIn(["load", "stages"], document.createNode(stages));
  }
  return document.toString();
}

/**
 * A new RunSpec with the install's defaults where it names none (GET /run-defaults): its
 * Prometheus, the one tuning sessions' runs use, so the run collects server-side series too, and
 * its price book as analysis.infra_pricing, so the run is priced.
 */
export function withRunDefaults(text: string, defaults: Partial<RunDefaults> | undefined): string {
  const document = parseDocument(text);
  if (document.errors.length > 0 || !defaults) return text;
  let changed = false;
  if (defaults.prometheus && !document.has("prometheus")) {
    document.set("prometheus", document.createNode(defaults.prometheus));
    changed = true;
  }
  if (defaults.infra_pricing && !document.hasIn(["analysis", "infra_pricing"])) {
    document.setIn(["analysis", "infra_pricing"], document.createNode(defaults.infra_pricing));
    changed = true;
  }
  return changed ? document.toString() : text;
}
