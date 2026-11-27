import { Text, Tooltip } from "@mantine/core";
import type { ObjectiveName } from "../../api/types";
import { formatObjective, type Estimate } from "./objectives";

/**
 * An objective estimate: the value with its 95% interval and how it was
 * found. Unavailable is said with the reason, never shown as zero.
 */
export function EstimateValue({ objective, estimate }: { objective: ObjectiveName; estimate: Estimate | undefined }) {
  if (estimate == null)
    return (
      <Text span c="dimmed">
        –
      </Text>
    );
  if (estimate.value == null) {
    return (
      <Tooltip label={estimate.unavailable_reason ?? "Unavailable"} multiline w={320}>
        <Text span c="dimmed">
          unavailable
        </Text>
      </Tooltip>
    );
  }
  const how =
    estimate.method === "repeats"
      ? `mean of ${estimate.samples} runs, Student t 95% interval`
      : estimate.method === "bootstrap"
        ? `bootstrap 95% interval over ${estimate.samples} requests`
        : "one run: no interval";
  return (
    <Tooltip label={boundText(how, estimate)} multiline w={300}>
      <span>
        {estimate.is_lower_bound ? "≥ " : estimate.is_upper_bound ? "≤ " : ""}
        {formatObjective(objective, estimate.value)}{" "}
        <Text span size="xs" c="dimmed">
          {estimate.ci_low != null && estimate.ci_high != null
            ? `${formatObjective(objective, estimate.ci_low)}–${formatObjective(objective, estimate.ci_high)}`
            : "single run"}
        </Text>
      </span>
    </Tooltip>
  );
}

// A run that never failed its SLOs is censored: better than measured, so a
// lower bound when more is better and an upper bound for a cost per unit.
function boundText(how: string, estimate: Estimate): string {
  if (estimate.is_lower_bound) return `${how}. A lower bound: some run never failed its SLOs.`;
  if (estimate.is_upper_bound)
    return `${how}. An upper bound: some run never failed its SLOs, so the cost may be lower.`;
  return how;
}
