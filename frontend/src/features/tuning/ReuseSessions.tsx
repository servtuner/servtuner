import { MultiSelect } from "@mantine/core";
import { useTuningSessions } from "../../api/hooks";
import type { ManagedTarget } from "../../api/types";
import { isActive } from "./session";

/**
 * Earlier sessions whose boots and runs seed this one: finished, on the same target,
 * with the same preset. The api also checks the SLOs, that the target is unedited since, and on a
 * host that its vLLM is the version those boots recorded.
 */
export function ReuseSessions({
  target,
  preset,
  reused,
  onReusedChange,
}: {
  target: ManagedTarget;
  preset: string | null;
  reused: string[];
  onReusedChange: (ids: string[]) => void;
}) {
  const sessions = useTuningSessions();
  const eligible = (sessions.data ?? []).filter(
    (session) => session.managed_target_id === target.id && session.request.preset === preset && !isActive(session),
  );
  if (eligible.length === 0) return null;
  return (
    <MultiSelect
      label="Reuse earlier sessions"
      description={
        target.spec.placement === "host"
          ? "Their boot results and runs seed this session, if the host still runs the vLLM version they measured"
          : "Their boot results and runs seed this session, so it skips what they already measured"
      }
      data={eligible.map((session) => ({
        value: session.id,
        label: `${session.experiment} (${session.runs_started} runs)`,
      }))}
      value={reused}
      onChange={onReusedChange}
      clearable
    />
  );
}
