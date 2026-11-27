import { Badge, Tooltip } from "@mantine/core";
import type { AnalysisResult } from "../api/types";

type StageQuality = AnalysisResult["quality"]["stages"][number];

/** Whether a stage's numbers count, next to them (frontend/CLAUDE.md, Constraints). */
export function StageValidity({ quality }: { quality: StageQuality | undefined }) {
  if (quality === undefined) return null;
  const reasons = (quality.issues ?? []).map((issue) => issue.message).join("; ");
  const [label, color] = quality.is_excluded
    ? ["Excluded", "red"]
    : !quality.is_valid
      ? ["Invalid", "orange"]
      : quality.is_client_bound
        ? ["Client-bound", "orange"]
        : ["Valid", "teal"];
  const badge = (
    <Badge color={color} variant="light" size="sm">
      {label}
    </Badge>
  );
  return reasons ? (
    <Tooltip label={reasons} multiline w={320}>
      {badge}
    </Tooltip>
  ) : (
    badge
  );
}
