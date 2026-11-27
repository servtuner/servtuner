import type { TuningSession } from "../../api/types";

type Status = TuningSession["status"];

/** How each session status reads. A Record, so a new status cannot go unlabelled. */
export const STATUS_BADGE: Record<Status, { label: string; color: string }> = {
  running: { label: "Running", color: "blue" },
  awaiting_approval: { label: "Awaiting approval", color: "yellow" },
  done: { label: "Done", color: "teal" },
  failed: { label: "Failed", color: "red" },
  cancelled: { label: "Cancelled", color: "gray" },
};

/** A host running servtuner launcher, added in host mode, then on to New session with it picked. */
export const ADD_HOST_FOR_TUNING = "/managed-targets?add=host&next=tuning";

export function isActive(session: Pick<TuningSession, "status">): boolean {
  return session.status === "running" || session.status === "awaiting_approval";
}

type Step = {
  phase?: string;
  proposed?: { action?: string; config_id?: string };
  waiting?: string;
  repeat?: number;
};

/** One line on what the session is doing now. */
export function describeStep(step: Step | null | undefined): string {
  if (!step) return "Finished";
  const proposed = step.proposed;
  const what =
    proposed?.action === "boot_probe" ? `boot probe of ${proposed.config_id}` : `runs of ${proposed?.config_id}`;
  switch (step.phase) {
    case "propose":
      return "Choosing the next configuration (servtuner propose)";
    case "approval":
      return `Waiting for approval: ${what}`;
    case "deploy":
      return `Deploying for the ${what}`;
    case "booting":
      return `Starting vLLM for the ${what}${step.waiting ? `: ${step.waiting}` : ""}`;
    case "running":
      return `Running repeat ${(step.repeat ?? 0) + 1} of ${proposed?.config_id}`;
    default:
      return step.phase ?? "Unknown";
  }
}
