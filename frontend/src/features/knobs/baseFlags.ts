import type { KnobCatalog, KnobDescription } from "../../api/types";

/** The flag a base flag line sets: "--max-model-len=4096" and "--max-model-len 4096" are
 * "--max-model-len"; a dotted config field ("-cc.mode=x") is its config flag; a value is null. */
export function flagOf(line: string): string | null {
  const word = line.trim().split(/\s+/)[0] ?? "";
  if (!word.startsWith("-")) return null;
  const name = word.split("=")[0]!;
  return name.startsWith("--") || name.startsWith("-") ? name.split(".")[0]! : name;
}

// vLLM's parser rewrites -O3 and -O=3 into --optimization-level before parsing.
const OPTIMIZATION_LEVEL = /^-O\d*$/;

/** The base flags vLLM at the catalog's version does not take: a typo, or a newer vLLM's flag. */
export function unknownFlags(lines: string[], catalog: KnobCatalog): string[] {
  const known = new Set(catalog.other_flags);
  for (const knob of catalog.knobs) for (const flag of spellingsOf(knob)) known.add(flag);
  return lines
    .map(flagOf)
    .filter((flag): flag is string => flag !== null && !known.has(flag) && !OPTIMIZATION_LEVEL.test(flag));
}

/** The base flag line a knob overrides at deploy (knobs come last, so the knob wins), if any. */
export function overriddenLine(lines: string[], knob: KnobDescription): string | null {
  const spellings = new Set(spellingsOf(knob));
  return lines.find((line) => spellings.has(flagOf(line) ?? "")) ?? null;
}

const spellingsOf = (knob: KnobDescription) => [knob.flag, ...knob.aliases, ...knob.negative_flags].filter(Boolean);
