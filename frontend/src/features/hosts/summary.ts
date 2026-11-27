import type { Host, HostCreate, HostPrice } from "../../api/types";
import { formatBytes, formatUsd, plural } from "../../components/format";

const BYTES_PER_GIB = 1073741824; // 2^30: the form takes memory in GiB

/** "1 × NVIDIA GeForce RTX 3070 (8 GiB) · 16 CPU cores · 31.25 GiB": what the machine is. */
export function hardwareLine(hardware: Host["hardware"]): string {
  if (!hardware) return "Hardware not recorded";
  const parts: string[] = [];
  const gpus = hardware.gpus ?? [];
  const byModel = new Map<string, { count: number; memory: number }>();
  for (const gpu of gpus) {
    const key = `${gpu.model}|${gpu.memory_bytes}`;
    byModel.set(key, { count: (byModel.get(key)?.count ?? 0) + 1, memory: gpu.memory_bytes });
  }
  for (const [key, { count, memory }] of byModel)
    parts.push(`${count} × ${key.split("|")[0]} (${formatBytes(memory)})`);
  if (gpus.length === 0) parts.push("no GPU recorded");
  if (hardware.cpu_cores) parts.push(plural(hardware.cpu_cores, "CPU core"));
  if (hardware.memory_bytes) parts.push(formatBytes(hardware.memory_bytes));
  return parts.join(" · ");
}

/** "$0.0762/h" with how it was worked out, or that the host is unpriced. */
export function priceLine(host: Pick<Host, "usd_per_hour" | "price_basis">): string {
  if (host.usd_per_hour == null) return "No price";
  const basis = host.price_basis && host.price_basis !== "given" ? ` (${host.price_basis})` : "";
  return `${formatUsd(host.usd_per_hour)}/h${basis}`;
}

/** The host form's fields, as text, for create and edit. */
export type HostFields = {
  name: string;
  note: string;
  gpuModel: string;
  gpuCount: string;
  gpuMemoryGib: string;
  cpuCores: string;
  memoryGib: string;
  priceKind: "none" | HostPrice["kind"];
  usdPerHour: string;
  purchaseUsd: string;
  amortizationHours: string;
  watts: string;
  usdPerKwh: string;
};

const gib = (bytes: number | null | undefined) =>
  bytes ? String(Math.round((bytes / BYTES_PER_GIB) * 100) / 100) : "";

export function hostFields(host?: Host): HostFields {
  const gpus = host?.hardware?.gpus ?? [];
  const price = host?.price;
  return {
    name: host?.name ?? "",
    note: host?.note ?? "",
    gpuModel: gpus[0]?.model ?? "",
    gpuCount: gpus.length > 0 ? String(gpus.length) : "",
    gpuMemoryGib: gib(gpus[0]?.memory_bytes),
    cpuCores: host?.hardware?.cpu_cores ? String(host.hardware.cpu_cores) : "",
    memoryGib: gib(host?.hardware?.memory_bytes),
    priceKind: price?.kind ?? "none",
    usdPerHour: price?.kind === "given" ? String(price.usd_per_hour) : "",
    purchaseUsd: price?.kind === "owned" ? String(price.purchase_usd) : "",
    amortizationHours: price?.kind === "owned" ? String(price.amortization_hours) : "",
    watts: price?.kind === "owned" ? String(price.watts) : "",
    usdPerKwh: price?.kind === "owned" ? String(price.usd_per_kwh) : "",
  };
}

const numberOf = (text: string) => (text.trim() === "" ? undefined : Number(text));

/**
 * The POST/PUT body. Hardware the launcher read is kept as it is unless the user changed the
 * typed summary (one GPU model, its count and memory); then it becomes manual.
 */
export function hostBody(fields: HostFields, existing?: Host): HostCreate {
  const before = hostFields(existing);
  const hardwareChanged = (["gpuModel", "gpuCount", "gpuMemoryGib", "cpuCores", "memoryGib"] as const).some(
    (key) => fields[key] !== before[key],
  );
  const count = numberOf(fields.gpuCount) ?? (fields.gpuModel.trim() ? 1 : 0);
  const gpuMemory = numberOf(fields.gpuMemoryGib);
  const memory = numberOf(fields.memoryGib);
  const hardware =
    existing?.hardware && !hardwareChanged
      ? asSent(existing.hardware)
      : {
          source: "manual" as const,
          gpus: fields.gpuModel.trim()
            ? Array.from({ length: count }, () => ({
                model: fields.gpuModel.trim(),
                memory_bytes: Math.round((gpuMemory ?? 0) * BYTES_PER_GIB),
                driver_version: null,
              }))
            : [],
          cpu_model: null,
          cpu_cores: numberOf(fields.cpuCores) ?? null,
          memory_bytes: memory ? Math.round(memory * BYTES_PER_GIB) : null,
          read_at: null,
        };
  const price: HostCreate["price"] =
    fields.priceKind === "given"
      ? { kind: "given", usd_per_hour: Number(fields.usdPerHour) }
      : fields.priceKind === "owned"
        ? {
            kind: "owned",
            purchase_usd: Number(fields.purchaseUsd),
            amortization_hours: Number(fields.amortizationHours),
            watts: Number(fields.watts),
            usd_per_kwh: Number(fields.usdPerKwh),
          }
        : null;
  return { name: fields.name.trim(), note: fields.note.trim() || null, hardware, price };
}

type SentHardware = NonNullable<HostCreate["hardware"]>;

/** Stored hardware as a body sends it back: every optional field present. */
function asSent(hardware: NonNullable<Host["hardware"]>): SentHardware {
  return {
    source: hardware.source,
    gpus: (hardware.gpus ?? []).map((gpu) => ({ ...gpu, driver_version: gpu.driver_version ?? null })),
    cpu_model: hardware.cpu_model ?? null,
    cpu_cores: hardware.cpu_cores ?? null,
    memory_bytes: hardware.memory_bytes ?? null,
    read_at: hardware.read_at ?? null,
  };
}

/** Why the form cannot be saved yet, or null. */
export function hostFormError(fields: HostFields): string | null {
  if (!/^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/.test(fields.name.trim()))
    return "Name: lower case letters, digits and dashes";
  if (fields.gpuModel.trim() && !(Number(fields.gpuMemoryGib) > 0)) return "Give the GPU's memory";
  if (fields.priceKind === "given" && !(Number(fields.usdPerHour) > 0)) return "Give the price per hour";
  if (
    fields.priceKind === "owned" &&
    !(Number(fields.purchaseUsd) > 0 && Number(fields.amortizationHours) > 0 && fields.watts && fields.usdPerKwh)
  )
    return "Give the purchase price, hours to spread it over, watts and electricity price";
  return null;
}
