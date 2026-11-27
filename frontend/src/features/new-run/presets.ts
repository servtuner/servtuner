import type { WorkloadPreset } from "../../api/types";
import type { Change } from "./TargetPicker";

/** Offered RPS of each stage of a starting ladder, as multiples of the preset's hint. */
export const LADDER_FACTORS = [1, 2, 4, 8];
/** Each starting stage lasts 90 s: above the 60 s minimum at a 5 s scrape. */
export const LADDER_STAGE_DURATION = "90s";

export function presetReference(preset: WorkloadPreset): string {
  return `${preset.name}@${preset.version}`;
}

export type Workload = { preset?: string; streaming?: boolean } | undefined;

/**
 * Choosing a preset: the workload becomes the
 * preset's name, with no explicit dataset, api or conversation (a preset is
 * never partly overridden); streaming stays the RunSpec's; the preset's SLOs
 * are filled in; and an engine the preset cannot run on is swapped for its
 * first one.
 */
export function presetChanges(preset: WorkloadPreset, workload: Workload, engine: string): Change[] {
  const changes: Change[] = [
    [["workload", "preset"], presetReference(preset)],
    [["workload", "dataset"], undefined],
    [["workload", "conversation"], undefined],
    [["workload", "api"], undefined],
    [["workload", "streaming"], workload?.streaming ?? true],
  ];
  for (const [name, value] of Object.entries(preset.slos)) {
    if (value !== undefined && value !== null) changes.push([["slos", name], value]);
  }
  if (!preset.engines.includes(engine as WorkloadPreset["engines"][number])) {
    changes.push([["engine", "type"], preset.engines[0]]);
  }
  return changes;
}

/** Customize: the preset's workload written out, so it can be edited; the name goes, so the hash says it is custom. */
export function customizeChanges(preset: WorkloadPreset): Change[] {
  return [
    [["workload", "preset"], undefined],
    [["workload", "api"], preset.workload.api ?? "chat"],
    [["workload", "dataset"], preset.workload.dataset],
    [["workload", "conversation"], preset.workload.conversation ?? undefined],
  ];
}

export function startingLadder(startRps: number): { rate: number; duration: string }[] {
  return LADDER_FACTORS.map((factor) => ({ rate: startRps * factor, duration: LADDER_STAGE_DURATION }));
}
