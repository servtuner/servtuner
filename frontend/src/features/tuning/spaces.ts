import { parseDocument, stringify } from "yaml";
import { LADDER_FACTORS } from "../new-run/presets";
import type { ManagedTarget, SearchSpaceTemplate } from "../../api/types";

/** Whether a starting space suits the target: its server, its placement, and a Kubernetes target's GPUs. */
export function fitsTarget(template: SearchSpaceTemplate, target: ManagedTarget): boolean {
  const { fits } = template;
  const server = target.spec.placement === "host" ? "vllm" : (target.spec.server ?? "vllm");
  if (!(fits.servers ?? ["vllm"]).includes(server)) return false;
  if (!fits.placements.includes(target.spec.placement ?? "kubernetes")) return false;
  if (target.spec.placement === "host") return true; // a host declares no GPU count
  const gpus = target.spec.gpus;
  return (fits.min_gpus == null || gpus >= fits.min_gpus) && (fits.max_gpus == null || gpus <= fits.max_gpus);
}

/** The templates that fit the target first, each group in the catalog's order. */
export function rankForTarget(templates: SearchSpaceTemplate[], target: ManagedTarget | undefined) {
  if (!target) return { fitting: [], others: templates };
  return {
    fitting: templates.filter((template) => fitsTarget(template, target)),
    others: templates.filter((template) => !fitsTarget(template, target)),
  };
}

export const templateKey = (template: SearchSpaceTemplate) => `${template.name}@${template.version}`;

/** The search space box's text for a template: where it came from, then its space as YAML. */
export function templateText(template: SearchSpaceTemplate): string {
  return `# Starting space ${templateKey(template)}: ${template.title}. Edit freely; servtuner propose checks it.\n${stringify(template.space)}`;
}

// Start near half the measured SLO-max, then up to just past it, where the knee is.
const MEASURED_PROBE_FACTORS = [0.5, 0.75, 1, 1.25];

const roundRate = (rps: number) => Math.max(0.1, Math.round(rps * 10) / 10);

/** Probe rates around a target's measured SLO-max for the preset (the measurement comes first). */
export function measuredProbeRates(sloMaxRps: number): number[] {
  return MEASURED_PROBE_FACTORS.map((factor) => roundRate(sloMaxRps * factor));
}

/** Probe rates from a preset's hand-written hint: its start rate, doubling (New run's ladder). */
export function hintProbeRates(startRps: number): number[] {
  return LADDER_FACTORS.map((factor) => roundRate(startRps * factor));
}

/** The space text with ladder.probe_rps set, its comments and other edits kept. */
export function withProbeRates(text: string, rates: number[]): string {
  const document = parseDocument(text);
  if (document.errors.length > 0) return text;
  document.setIn(["ladder", "probe_rps"], document.createNode(rates, { flow: true }));
  return document.toString();
}

/** The space text with its objective set, its comments and other edits kept. */
export function withObjective(text: string, objective: string): string {
  const document = parseDocument(text);
  if (document.errors.length > 0) return text;
  document.set("objective", objective);
  return document.toString();
}
