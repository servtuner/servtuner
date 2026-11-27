import { Text, Tooltip } from "@mantine/core";
import type { BootstrapInterval } from "../api/types";
import { formatSeconds, formatSecondsRange } from "./format";

/**
 * A statistic with its 95% bootstrap interval. Too few samples renders as
 * "n/a (n=…)" with the reason on hover, never as a number.
 */
export function IntervalValue({ interval }: { interval: BootstrapInterval | null | undefined }) {
  if (interval == null)
    return (
      <Text span c="dimmed">
        –
      </Text>
    );
  if (interval.value == null) {
    return (
      <Tooltip label={interval.insufficient_samples_reason ?? "Too few samples"} multiline w={280}>
        <Text span c="dimmed">
          n/a (n={interval.samples})
        </Text>
      </Tooltip>
    );
  }
  return (
    <Tooltip label={`95% interval from ${interval.samples} samples`}>
      <span>
        {formatSeconds(interval.value)}{" "}
        <Text span size="xs" c="dimmed">
          {formatSecondsRange(interval.ci_low, interval.ci_high)}
        </Text>
      </span>
    </Tooltip>
  );
}
