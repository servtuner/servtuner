import type { CapacityTable } from "../../api/types";
import { formatUsd } from "../../components/format";

type CostLine = { text: string; hover?: string };
type TokenCost = NonNullable<CapacityTable["token_cost"]>;

const TOKEN_METHOD: Record<NonNullable<TokenCost["method"]>, string> = {
  router_metric: "From the router's own cost counter",
  datasheet: "Tokens × the datasheet's prices",
  manual: "Tokens × the prices given for this target",
};

/** " (prices as of 2026-11-26, from the price book)": the datasheet's date, when known. */
function pricesAsOf(token: TokenCost): string {
  if (!token.datasheet_fetched_at || token.method === "manual") return "";
  const from = token.datasheet_source === "price_book" ? ", from the price book" : "";
  return ` (prices as of ${token.datasheet_fetched_at.slice(0, 10)}${from})`;
}

/** One line per kind of cost, with how it was worked out on hover. */
export function costLines(table: CapacityTable): CostLine[] {
  const lines: CostLine[] = [];
  const infra = table.infra_cost;
  if (infra?.is_priced && infra.usd_per_replica_hour != null) {
    const discount = infra.discount != null && infra.discount !== 1 ? `, ×${infra.discount} discount` : "";
    lines.push({
      text: `${formatUsd(infra.usd_per_replica_hour)} per replica-hour (${infra.source} prices${discount})`,
      hover: infra.basis ?? undefined,
    });
    if (infra.usd_per_1k_requests != null)
      lines.push({
        text: `Infrastructure at the SLO-max: ${formatUsd(infra.usd_per_1k_requests)} per 1k requests, ${formatUsd(infra.usd_per_1m_output_tokens)} per 1M output tokens`,
        hover: "A replica's hour over what it serves in an hour at its SLO-max RPS",
      });
    else if (infra.unit_cost_reason) lines.push({ text: `No cost per request: ${infra.unit_cost_reason}` });
  } else if (infra) lines.push({ text: `Infrastructure unpriced: ${infra.reason ?? "no reason given"}` });
  const token = table.token_cost;
  if (token?.is_priced && token.usd_per_1k_requests != null)
    lines.push({
      text: `Tokens: ${formatUsd(token.usd_per_1k_requests)} per 1k requests, ${formatUsd(token.usd_per_1m_output_tokens)} per 1M output tokens${pricesAsOf(token)}`,
      hover: TOKEN_METHOD[token.method ?? "datasheet"],
    });
  else if (token) lines.push({ text: `Tokens unpriced: ${token.reason ?? "no reason given"}` });
  if (lines.length === 0) lines.push({ text: "Not priced: the RunSpec names no prices (analysis.infra_pricing)" });
  return lines;
}
