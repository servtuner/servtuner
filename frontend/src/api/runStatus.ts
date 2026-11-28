import type { RunStatus, TargetType } from "./types";

// Keyed by the generated unions, so a status or target type added to the
// contract fails the build until it is handled here.
export const RUN_STATUS_META = {
  // A follow-up run waits for the user to apply the change it tests: it is not
  // over, but nothing changes until they confirm, so it is not polled.
  awaiting_config: {
    label: "Awaiting config change",
    color: "yellow",
    isTerminal: false,
  },
  pending: { label: "Pending", color: "gray", isTerminal: false },
  launching: { label: "Launching", color: "blue", isTerminal: false },
  running: { label: "Running", color: "blue", isTerminal: false },
  collecting: { label: "Collecting", color: "cyan", isTerminal: false },
  succeeded: { label: "Succeeded", color: "green", isTerminal: true },
  failed: { label: "Failed", color: "red", isTerminal: true },
  cancelled: { label: "Cancelled", color: "gray", isTerminal: true },
} satisfies Record<RunStatus, { label: string; color: string; isTerminal: boolean }>;

export const TARGET_TYPE_LABELS = {
  vllm: "vLLM",
  litellm: "LiteLLM",
  bifrost: "Bifrost",
  "openai-compatible": "OpenAI-compatible",
  sglang: "SGLang",
} satisfies Record<TargetType, string>;

// The API accepts any target_type string, so unknown values show as they are.
export function targetTypeLabel(targetType: string): string {
  return TARGET_TYPE_LABELS[targetType as TargetType] ?? targetType;
}

export const RUN_STATUSES = Object.keys(RUN_STATUS_META) as RunStatus[];
export const TARGET_TYPES = Object.keys(TARGET_TYPE_LABELS) as TargetType[];

export const RUN_POLL_INTERVAL_MILLISECONDS = 2000;

export function isTerminal(status: RunStatus): boolean {
  return RUN_STATUS_META[status].isTerminal;
}

// Polls every 2s while any run is in progress, then stops.
// A run awaiting a config change is not in progress: only the user moves it.
export function pollIntervalFor(statuses: readonly RunStatus[] | undefined): number | false {
  return statuses?.some((status) => !isTerminal(status) && status !== "awaiting_config")
    ? RUN_POLL_INTERVAL_MILLISECONDS
    : false;
}
