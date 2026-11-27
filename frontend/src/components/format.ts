// Number formatting shared by tables and charts: units always shown, three
// significant figures, milliseconds below one second.

export function formatSeconds(seconds: number | null | undefined): string {
  if (seconds == null) return "–";
  if (seconds < 1) return `${trim((seconds * 1000).toPrecision(3))} ms`;
  return `${trim(seconds.toPrecision(3))} s`;
}

/** A range in one unit, chosen by its upper end: "125–126 ms", "1.2–1.4 s". */
export function formatSecondsRange(low: number | null | undefined, high: number | null | undefined): string {
  if (low == null || high == null) return "–";
  const [scale, unit] = high < 1 ? [1000, "ms"] : [1, "s"];
  return `${trim((low * scale).toPrecision(3))}–${trim((high * scale).toPrecision(3))} ${unit}`;
}

export function formatNumber(value: number | null | undefined, digits = 4): string {
  if (value == null) return "–";
  if (Math.abs(value) >= 10 ** digits) return Math.round(value).toLocaleString("en-US");
  return trim(value.toPrecision(digits));
}

const BYTE_UNITS = ["B", "KiB", "MiB", "GiB", "TiB"];
const BYTES_PER_STEP = 1024; // binary units, like Kubernetes Mi and Gi

/**
 * Bytes in the largest binary unit that keeps the number at 1 or more, with
 * the number and unit apart so a caller can style the unit: 1536 → "1.5", "KiB".
 * Three significant figures, or four from 1000 up so 1023 B is not "1020 B".
 */
export function scaleBytes(bytes: number): { value: string; unit: string } {
  let value = bytes;
  let unit = 0;
  while (Math.abs(value) >= BYTES_PER_STEP && unit < BYTE_UNITS.length - 1) {
    value /= BYTES_PER_STEP;
    unit++;
  }
  return { value: formatNumber(value, Math.abs(value) >= 1000 ? 4 : 3), unit: BYTE_UNITS[unit]! };
}

export function formatBytes(bytes: number | null | undefined): string {
  if (bytes == null) return "–";
  const { value, unit } = scaleBytes(bytes);
  return `${value} ${unit}`;
}

/** A clock frequency in MHz below 1 GHz, else GHz: "1.9 GHz", "810 MHz". */
export function formatHertz(hertz: number | null | undefined): string {
  if (hertz == null) return "–";
  if (Math.abs(hertz) >= 1e9) return `${formatNumber(hertz / 1e9, 3)} GHz`;
  return `${formatNumber(hertz / 1e6, 3)} MHz`;
}

export function formatPercent(fraction: number | null | undefined): string {
  if (fraction == null) return "–";
  return `${formatNumber(fraction * 100, 2)}%`;
}

function trim(text: string): string {
  return text.includes("e") ? String(Number(text)) : text.replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "");
}

export function formatCores(cores: number | null | undefined): string {
  if (cores == null) return "–";
  return `${formatNumber(cores)} ${cores === 1 ? "core" : "cores"}`;
}

/** US dollars: cents from a dollar up, three significant figures below ($0.0178). */
export function formatUsd(usd: number | null | undefined): string {
  if (usd == null) return "–";
  const text =
    Math.abs(usd) >= 1
      ? usd.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })
      : formatNumber(usd, 3);
  return "$" + text;
}

/** "1 run", "3 runs". */
export function plural(count: number, one: string, many = `${one}s`) {
  return `${count} ${count === 1 ? one : many}`;
}
