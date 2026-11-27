import type { KnobDescription, VllmKnobs } from "../../api/types";

export type KnobValue = string | number | boolean;

/** A knob being edited: its value is null until the user sets one. */
export type KnobEntry = { name: string; value: KnobValue | null };

export const NUMERIC_KINDS: ReadonlySet<KnobDescription["kind"]> = new Set(["integer", "number", "size"]);

export function entriesOf(knobs: VllmKnobs | undefined): KnobEntry[] {
  return Object.entries(knobs ?? {}).map(([name, value]) => ({ name, value }));
}

/** The knobs to deploy, or null while any entry has no value or breaks its limits. */
export function knobsOf(entries: KnobEntry[], catalog: Map<string, KnobDescription>): VllmKnobs | null {
  const knobs: VllmKnobs = {};
  for (const entry of entries) {
    if (entry.value === null || problemOf(entry, catalog.get(entry.name))) return null;
    knobs[entry.name] = entry.value;
  }
  return knobs;
}

/** What a new knob starts at: vLLM's own default when it has one of the right type, else unset
 * (vLLM decides at start-up, and servtuner never invents a number for it). */
export function initialValue(knob: KnobDescription): KnobValue | null {
  if (knob.kind === "switch") return true;
  const fallback = knob.default;
  if (NUMERIC_KINDS.has(knob.kind)) return typeof fallback === "number" ? fallback : null;
  if (knob.kind === "boolean") return typeof fallback === "boolean" ? fallback : true;
  return typeof fallback === "string" && fallback !== "" ? fallback : null;
}

/** Why a value is not one vLLM would take, by the limits in its source; null when it is. */
export function problemOf(entry: KnobEntry, knob: KnobDescription | undefined): string | null {
  if (entry.value === null || entry.value === "") return "Set a value";
  if (!knob) return null; // a knob the catalog lacks: the api checks it
  if (NUMERIC_KINDS.has(knob.kind)) {
    if (typeof entry.value !== "number" || !Number.isFinite(entry.value)) return "A number";
    if (knob.kind !== "number" && !Number.isInteger(entry.value)) return "A whole number";
    const { ge, gt, le, lt } = knob.limits ?? {};
    if (typeof ge === "number" && entry.value < ge) return `At least ${ge}`;
    if (typeof gt === "number" && entry.value <= gt) return `More than ${gt}`;
    if (typeof le === "number" && entry.value > le) return `At most ${le}`;
    if (typeof lt === "number" && entry.value >= lt) return `Less than ${lt}`;
  }
  if (knob.kind === "choice" && knob.choices && !knob.choices.map(String).includes(String(entry.value)))
    return `One of ${knob.choices.join(", ")}`;
  return null;
}

/** The limits as a reader would say them: "more than 0 and at most 1". */
export function describeLimits(knob: KnobDescription): string | null {
  const { ge, gt, le, lt } = knob.limits ?? {};
  const parts = [
    typeof ge === "number" && `at least ${ge}`,
    typeof gt === "number" && `more than ${gt}`,
    typeof le === "number" && `at most ${le}`,
    typeof lt === "number" && `less than ${lt}`,
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" and ") : null;
}

/** A chip's value: On/Off for booleans, the value itself otherwise. */
export function displayValue(entry: KnobEntry, knob: KnobDescription | undefined): string {
  if (entry.value === null) return "not set";
  if (typeof entry.value === "boolean" || knob?.kind === "boolean" || knob?.kind === "switch")
    return entry.value === true || entry.value === "true" ? "on" : "off";
  return String(entry.value);
}

/** Whether a catalog knob matches what the user typed in the picker: its label, flag, aliases or
 * name, so both "max seq" and "--max-num-seqs" find it. */
export function matchesSearch(knob: KnobDescription, search: string): boolean {
  const words = search.toLowerCase().trim().split(/\s+/).filter(Boolean);
  const haystack = [knob.label, knob.flag, knob.name, ...knob.aliases].join(" ").toLowerCase();
  return words.every((word) => haystack.includes(word));
}
