import type { Verdict } from "../../api/types";

// How each verdict of api/schedules.py reads on screen.
export const VERDICT_META: Record<Verdict, { label: string; color: string }> = {
  regressed: { label: "Regressed", color: "red" },
  improved: { label: "Improved", color: "teal" },
  steady: { label: "Steady", color: "gray" },
  baseline: { label: "Baseline", color: "blue" },
  pending: { label: "Running", color: "yellow" },
  failed: { label: "Failed", color: "orange" },
  unmeasured: { label: "No SLO-max", color: "gray" },
  no_baseline: { label: "No baseline", color: "gray" },
};

/** "every 24 hours", "every 7 days" */
export function describeInterval(hours: number): string {
  const HOURS_PER_DAY = 24;
  if (hours % HOURS_PER_DAY === 0) {
    const days = hours / HOURS_PER_DAY;
    return days === 1 ? "every day" : `every ${days} days`;
  }
  return hours === 1 ? "every hour" : `every ${hours} hours`;
}
