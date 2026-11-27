/** A build that no release stamped, on either side: the api reports the same value. */
export const DEV_VERSION = "0.0.0-dev";

/** The release this UI was built from. */
export const UI_VERSION: string = import.meta.env.VITE_SERVTUNER_VERSION || DEV_VERSION;

/** True when two released builds differ; a development build on either side is never a mismatch. */
export function versionsDiffer(uiVersion: string, apiVersion: string): boolean {
  return uiVersion !== DEV_VERSION && apiVersion !== DEV_VERSION && uiVersion !== apiVersion;
}

/** Enough of a commit to find it. */
export function shortCommit(commit: string): string {
  return /^[0-9a-f]{40}$/.test(commit) ? commit.slice(0, 7) : commit;
}
