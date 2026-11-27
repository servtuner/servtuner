import { Badge } from "@mantine/core";
import { RUN_STATUS_META } from "../api/runStatus";
import type { RunStatus } from "../api/types";

export function StatusBadge({ status }: { status: RunStatus }) {
  const { label, color } = RUN_STATUS_META[status];
  return <Badge color={color}>{label}</Badge>;
}
