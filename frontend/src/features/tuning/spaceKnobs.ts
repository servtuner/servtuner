import { parseDocument, type YAMLMap, type YAMLSeq } from "yaml";
import type { KnobDescription, VllmKnobs } from "../../api/types";
import { displayValue, initialValue, type KnobValue, problemOf } from "../knobs/knobValues";

/** The search's stages, in the order servtuner propose runs them (search-space.schema.json). */
export const TIERS = [
  { value: "feasibility", label: "Feasibility: boot checks, no load" },
  { value: "structural", label: "Structural" },
  { value: "scheduler", label: "Scheduler: the advisors move it" },
  { value: "fine_tuning", label: "Fine tuning" },
] as const;

export type Tier = (typeof TIERS)[number]["value"];

/** One tuned knob of a space, as space.yaml spells it: values, or bounds for a range knob. */
export type TunedKnob = {
  name: string;
  tier: Tier;
  values?: KnobValue[];
  min?: number;
  max?: number;
  default?: KnobValue;
};

export const isRange = (knob: TunedKnob) => knob.values === undefined;

/** The tuned knobs of a parsed space; entries without a name are left to servtuner propose to refuse. */
export function tunedKnobsOf(space: Record<string, unknown>): TunedKnob[] {
  const knobs = Array.isArray(space.knobs) ? (space.knobs as unknown[]) : [];
  return knobs.filter(
    (knob): knob is TunedKnob =>
      typeof knob === "object" && knob !== null && typeof (knob as TunedKnob).name === "string",
  );
}

/** A new tuned knob: fine tuning, both states of a boolean, else vLLM's default as its one value. */
export function newTunedKnob(knob: KnobDescription): TunedKnob {
  if (knob.kind === "boolean") return { name: knob.name, tier: "fine_tuning", values: [false, true] };
  const start = initialValue(knob);
  return { name: knob.name, tier: "fine_tuning", values: start === null ? [] : [start] };
}

/** Why servtuner propose would refuse a tuned knob, checked against vLLM's limits; null when it is fine. */
export function tunedProblem(knob: TunedKnob, description: KnobDescription | undefined): string | null {
  const check = (value: KnobValue | null | undefined) =>
    problemOf({ name: knob.name, value: value ?? null }, description);
  if (isRange(knob)) {
    if (typeof knob.min !== "number" || typeof knob.max !== "number") return "Set both bounds";
    if (knob.min > knob.max) return "The lower bound is above the upper";
    return check(knob.min) ?? check(knob.max);
  }
  if (!knob.values || knob.values.length === 0) return "Set at least one value";
  for (const value of knob.values) {
    const problem = check(value);
    if (problem) return problem;
  }
  return null;
}

/** A chip's value: "8–128" for a range, "auto or fp8" for values. */
export function describeTuned(knob: TunedKnob, description: KnobDescription | undefined): string {
  if (isRange(knob)) return `${knob.min ?? "?"}–${knob.max ?? "?"}`;
  if (!knob.values || knob.values.length === 0) return "no values";
  return knob.values.map((value) => displayValue({ name: knob.name, value }, description)).join(" or ");
}

// The starting spaces' own style: "- {name: vllm.x, tier: scheduler, min: 16, max: 512}", never folded.
const ONE_LINE_PER_KNOB = { lineWidth: 0, flowCollectionPadding: false } as const;

/** The space text with its tuned knobs replaced, one flow map per knob, other edits and comments kept. */
export function withTunedKnobs(text: string, knobs: TunedKnob[]): string {
  const document = parseDocument(text);
  if (document.errors.length > 0) return text;
  const node = document.createNode(knobs) as YAMLSeq<YAMLMap>;
  for (const item of node.items) item.flow = true;
  document.set("knobs", node);
  return document.toString(ONE_LINE_PER_KNOB);
}

/** The space text with its fixed (base) knobs replaced; none removes the key. */
export function withBase(text: string, base: Record<string, KnobValue | null>): string {
  const document = parseDocument(text);
  if (document.errors.length > 0) return text;
  if (Object.keys(base).length === 0) document.delete("base");
  else document.set("base", base);
  return document.toString();
}

/** The fixed knobs of a parsed space. */
export function baseOf(space: Record<string, unknown>): VllmKnobs | undefined {
  const base = space.base;
  return typeof base === "object" && base !== null && !Array.isArray(base) ? (base as VllmKnobs) : undefined;
}
