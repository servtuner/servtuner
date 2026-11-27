import type { Recommendations } from "../../api/types";

type Recommendation = Recommendations["recommendations"][number];

/** True when a recommendation is a knob change with a value: something a run can test. */
export function canFollowUp(recommendation: Recommendation): boolean {
  return recommendation.subject.kind === "knob" && recommendation.recommended != null;
}
