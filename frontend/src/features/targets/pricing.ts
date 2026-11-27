import type { SavedTarget, TargetCreate } from "../../api/types";
import { formatUsd } from "../../components/format";

/** How a saved target is paid for, as the form's text fields. */
export type PricingFields = {
  hourly: "none" | "host" | "node_type";
  hostId: string;
  nodeType: string;
  gpuCount: string;
  gpuModel: string;
  token: "none" | "datasheet" | "manual";
  url: string;
  inputPer1m: string;
  outputPer1m: string;
};

type SavedPricing = NonNullable<SavedTarget["pricing"]>;
type PricingBody = NonNullable<TargetCreate["pricing"]>;

export function pricingFields(pricing?: SavedPricing | null): PricingFields {
  const hourly = pricing?.hourly;
  const token = pricing?.token;
  return {
    hourly: hourly?.kind ?? "none",
    hostId: hourly?.kind === "host" ? hourly.host_id : "",
    nodeType: hourly?.kind === "node_type" ? hourly.node_type : "",
    gpuCount: hourly?.kind === "node_type" ? String(hourly.gpu_count) : "1",
    gpuModel: hourly?.kind === "node_type" ? (hourly.gpu_model ?? "") : "",
    token: token?.source ?? "none",
    url: token?.source === "datasheet" ? (token.url ?? "") : "",
    inputPer1m: token?.source === "manual" ? String(token.usd_per_1m_input_tokens) : "",
    outputPer1m: token?.source === "manual" ? String(token.usd_per_1m_output_tokens) : "",
  };
}

const number = (text: string) => (text.trim() === "" ? Number.NaN : Number(text));

/** What stops the prices from saving, or null. */
export function pricingError(fields: PricingFields): string | null {
  if (fields.hourly === "host" && !fields.hostId) return "Pick the host it runs on";
  if (fields.hourly === "node_type") {
    if (!fields.nodeType) return "Pick a node type from the price book";
    const gpus = number(fields.gpuCount);
    if (!(Number.isInteger(gpus) && gpus >= 1)) return "A node type is priced by the GPUs it holds: 1 or more";
  }
  if (fields.token === "manual" && !(number(fields.inputPer1m) >= 0 && number(fields.outputPer1m) >= 0))
    return "Give both token prices per 1M tokens, 0 or more";
  return null;
}

/** The TargetCreate pricing block, or null when it names nothing. */
export function pricingBody(fields: PricingFields): PricingBody | null {
  const hourly: PricingBody["hourly"] =
    fields.hourly === "host"
      ? { kind: "host", host_id: fields.hostId }
      : fields.hourly === "node_type"
        ? {
            kind: "node_type",
            node_type: fields.nodeType,
            gpu_count: Number(fields.gpuCount),
            gpu_model: fields.gpuModel.trim() || null,
          }
        : null;
  const token: PricingBody["token"] =
    fields.token === "datasheet"
      ? { source: "datasheet", url: fields.url.trim() || null }
      : fields.token === "manual"
        ? {
            source: "manual",
            usd_per_1m_input_tokens: Number(fields.inputPer1m),
            usd_per_1m_output_tokens: Number(fields.outputPer1m),
          }
        : null;
  return hourly || token ? { hourly, token } : null;
}

/** "By the hour as host gpu-box; by the token at $0.20 in / $0.80 out per 1M": for the target's page. */
export function pricingLine(target: SavedTarget, hostName: (id: string) => string | undefined): string {
  const parts: string[] = [];
  const hourly = target.pricing?.hourly;
  if (hourly?.kind === "host") parts.push(`by the hour as host ${hostName(hourly.host_id) ?? hourly.host_id}`);
  if (hourly?.kind === "node_type")
    parts.push(`by the hour as ${hourly.gpu_count} GPU of node type ${hourly.node_type} (price book)`);
  const token = target.pricing?.token;
  if (token?.source === "datasheet") parts.push("by the token from the datasheet");
  if (token?.source === "manual")
    parts.push(
      `by the token at ${formatUsd(token.usd_per_1m_input_tokens)} in / ${formatUsd(token.usd_per_1m_output_tokens)} out per 1M`,
    );
  if (parts.length === 0) return "Not priced: its runs carry no cost unless their spec names prices";
  const text = parts.join("; ");
  return `Priced ${text}`;
}
