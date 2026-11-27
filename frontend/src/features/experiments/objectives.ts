import { describeKnobs, type KnobText } from "../knobs/knobText";
import type { Comparison, ObjectiveName } from "../../api/types";
import { formatNumber, formatSeconds, formatUsd } from "../../components/format";

// Plain names for the comparison objectives.
export const OBJECTIVE_LABELS: Record<ObjectiveName, string> = {
  slo_max_rps: "SLO-max RPS",
  slo_rps_per_replica: "SLO RPS per replica",
  slo_rps_per_core: "SLO RPS per CPU core",
  slo_rps_per_gpu: "SLO RPS per GPU",
  tokens_per_s_per_gpu: "Output tokens/s per GPU",
  p95_ttft_at_target_rps: "p95 TTFT at the target RPS",
  infra_cost_per_1k_requests: "Infrastructure cost per 1k requests",
  infra_cost_per_1m_output_tokens: "Infrastructure cost per 1M output tokens",
  cost_per_1k_requests: "Token cost per 1k requests",
  cost_per_1m_output_tokens: "Token cost per 1M output tokens",
};

type Configuration = Comparison["configurations"][number];
export type Estimate = Configuration["objectives"][number]["estimate"];

export function estimateOf(configuration: Configuration, objective: ObjectiveName): Estimate | undefined {
  return configuration.objectives.find((entry) => entry.objective === objective)?.estimate;
}

export function formatObjective(objective: ObjectiveName, value: number | null | undefined): string {
  if (objective === "p95_ttft_at_target_rps") return formatSeconds(value);
  if (objective.startsWith("infra_cost_") || objective.startsWith("cost_")) return formatUsd(value);
  return formatNumber(value);
}

/** A configuration's name for charts and tables: its varied knobs' values ("Max sequences 128"). */
export function knobLabel(text: KnobText, knobs: Record<string, unknown>, varied: string[]): string {
  return describeKnobs(text, knobs, varied);
}
