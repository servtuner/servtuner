import type { PriceBook, PriceBookResource } from "../../api/types";
import { formatUsd, plural } from "../../components/format";

/** The price book form's fields, as text. */
export type NodeRow = { key: string; usdPerHour: string; cores: string; memoryGib: string; gpus: string };
export type GpuRow = { key: string; usdPerHour: string };
export type PriceBookFields = {
  source: PriceBook["source"];
  discount: string;
  awsRegion: string;
  nodes: NodeRow[];
  gpus: GpuRow[];
  datasheetUrl: string;
};

export const EMPTY_NODE: NodeRow = { key: "", usdPerHour: "", cores: "", memoryGib: "", gpus: "" };
export const EMPTY_GPU: GpuRow = { key: "", usdPerHour: "" };
const AWS_REGION = /^[a-z]{2}(-[a-z]+)+-\d+$/;

export function priceBookFields(book?: PriceBook): PriceBookFields {
  return {
    source: book?.source ?? "none",
    discount: String(book?.discount ?? 1),
    awsRegion: book?.aws_region ?? "",
    nodes: Object.entries(book?.nodes ?? {}).map(([key, price]) => ({
      key,
      usdPerHour: String(price.usd_per_hour),
      cores: String(price.cores),
      memoryGib: String(price.memory_gib),
      gpus: price.gpus ? String(price.gpus) : "",
    })),
    gpus: Object.entries(book?.gpus ?? {}).map(([key, price]) => ({ key, usdPerHour: String(price.usd_per_hour) })),
    datasheetUrl: book?.datasheet_url ?? "",
  };
}

const isBlank = (row: Record<string, string>) => Object.values(row).every((value) => value.trim() === "");
const number = (text: string) => (text.trim() === "" ? Number.NaN : Number(text));

/** What stops the form from saving, or null. Blank rows are ignored. */
export function priceBookError(fields: PriceBookFields): string | null {
  const discount = number(fields.discount);
  if (!(discount > 0 && discount <= 1)) return "The discount multiplier is above 0 and at most 1";
  if (fields.source === "aws" && !AWS_REGION.test(fields.awsRegion.trim()))
    return "Name the AWS region, e.g. us-east-1";
  const nodes = fields.nodes.filter((row) => !isBlank(row));
  const gpus = fields.gpus.filter((row) => !isBlank(row));
  for (const row of nodes) {
    if (!row.key.trim()) return "Every node type needs its instance type";
    if (!(number(row.usdPerHour) >= 0)) return `${row.key}: a price per hour of 0 or more`;
    if (!(number(row.cores) > 0) || !(number(row.memoryGib) > 0)) return `${row.key}: its cores and memory`;
    if (row.gpus.trim() && !Number.isInteger(number(row.gpus))) return `${row.key}: a whole number of GPUs`;
  }
  for (const row of gpus) {
    if (!row.key.trim()) return "Every GPU price needs its model";
    if (!(number(row.usdPerHour) >= 0)) return `${row.key}: a price per GPU-hour of 0 or more`;
  }
  const keys = [...nodes, ...gpus].map((row) => row.key.trim());
  if (new Set(nodes.map((row) => row.key.trim())).size < nodes.length) return "A node type is listed twice";
  if (new Set(gpus.map((row) => row.key.trim())).size < gpus.length) return "A GPU model is listed twice";
  if (fields.source === "static" && keys.length === 0) return "Price at least one node type or GPU model";
  if (fields.datasheetUrl.trim() && !/^https?:\/\/\S+$/.test(fields.datasheetUrl.trim()))
    return "The datasheet URL starts with http:// or https://";
  return null;
}

/** The PUT /price-book body. The tables are kept whatever the source, so switching back keeps them. */
export function priceBookBody(fields: PriceBookFields): PriceBook {
  const body: PriceBook = {
    source: fields.source,
    discount: Number(fields.discount),
    nodes: Object.fromEntries(
      fields.nodes
        .filter((row) => !isBlank(row))
        .map((row) => [
          row.key.trim(),
          {
            usd_per_hour: Number(row.usdPerHour),
            cores: Number(row.cores),
            memory_gib: Number(row.memoryGib),
            ...(row.gpus.trim() ? { gpus: Number(row.gpus) } : {}),
          },
        ]),
    ),
    gpus: Object.fromEntries(
      fields.gpus
        .filter((row) => !isBlank(row))
        .map((row) => [row.key.trim(), { usd_per_hour: Number(row.usdPerHour) }]),
    ),
  };
  if (fields.awsRegion.trim()) body.aws_region = fields.awsRegion.trim();
  if (fields.datasheetUrl.trim()) body.datasheet_url = fields.datasheetUrl.trim();
  return body;
}

/** "AWS Price List in eu-central-1, at 70% of list": how new runs are priced, in one line. */
export function priceBookLine(book: PriceBook): string {
  const discount =
    book.discount != null && book.discount !== 1 ? `, at ${Math.round(book.discount * 100)}% of list` : "";
  if (book.source === "aws") return `AWS Price List in ${book.aws_region}${discount}`;
  if (book.source === "static") {
    const parts = [
      Object.keys(book.nodes ?? {}).length > 0 ? plural(Object.keys(book.nodes ?? {}).length, "node type") : null,
      Object.keys(book.gpus ?? {}).length > 0 ? plural(Object.keys(book.gpus ?? {}).length, "GPU model") : null,
    ].filter(Boolean);
    return `Price table: ${parts.join(" and ")}${discount}`;
  }
  return "Not priced: new runs on Kubernetes carry no infrastructure cost";
}

export const usdPerHour = (value: number) => `${formatUsd(value)}/h`;

/** Where runs priced by the token get their datasheet, in one line. */
export function datasheetLine(book: PriceBookResource): string {
  const sheet = book.datasheet;
  if (sheet?.source === "upload")
    return `Uploaded datasheet: ${plural(sheet.priced_entries ?? 0, "priced model")}, sha256 ${sheet.sha256?.slice(0, 12)}…, uploaded ${sheet.uploaded_at?.slice(0, 10)}`;
  if (sheet?.source === "url") return `Datasheet from ${sheet.url}`;
  return book.offline
    ? "No datasheet: this install is offline, so upload one for runs priced by the token"
    : "No datasheet: runs priced by the token fetch the public one (getbifrost.ai)";
}
