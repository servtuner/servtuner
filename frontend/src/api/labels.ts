// Run labels the api sets, read by the UI to link a run to what made it. The api owns the
// names: managed_targets.MANAGED_TARGET_RUN_LABEL and tuning.SESSION_LABEL.

/** A run of a managed vLLM (Benchmark, or a tuning session): the target's id, lower case. */
export const MANAGED_TARGET_RUN_LABEL = "servtuner.managed-target";
/** A run a tuning session started: the session's id, lower case. */
export const TUNING_SESSION_LABEL = "servtuner.dev/tuning-session";

/** Labels carry ULIDs in lower case; ULIDs are case-insensitive and the routes use upper case. */
export const idFromLabel = (value: string | undefined) => value?.toUpperCase();
