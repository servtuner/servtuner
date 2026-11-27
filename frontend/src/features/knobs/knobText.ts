import { useMemo } from "react";
import { useKnobsByName } from "../../api/hooks";
import type { KnobDescription } from "../../api/types";
import { displayValue, type KnobValue } from "./knobValues";

/** How a knob reads to people: the catalog's label and value spelling, the flag for hover. A knob
 * the catalog lacks (bifrost.*, or before GET /knobs answers) reads as its name. */
export type KnobText = {
  label: (name: string) => string;
  flag: (name: string) => string;
  value: (name: string, value: unknown) => string;
};

export function knobTextOf(catalog: Map<string, KnobDescription>): KnobText {
  return {
    label: (name) => catalog.get(name)?.label ?? name,
    flag: (name) => catalog.get(name)?.flag || name,
    value: (name, value) =>
      value === undefined
        ? "unset"
        : typeof value === "object" && value !== null
          ? JSON.stringify(value)
          : displayValue({ name, value: value as KnobValue | null }, catalog.get(name)),
  };
}

export function useKnobText(): KnobText {
  const { catalog } = useKnobsByName();
  return useMemo(() => knobTextOf(catalog), [catalog]);
}

/** Knob values as "Max sequences 128, Prefix caching on", for the given names (else all). */
export function describeKnobs(text: KnobText, knobs: Record<string, unknown>, names?: string[]): string {
  const shown = names && names.length > 0 ? names : Object.keys(knobs);
  return shown.map((name) => `${text.label(name)} ${text.value(name, knobs[name])}`).join(", ") || "no knobs";
}
