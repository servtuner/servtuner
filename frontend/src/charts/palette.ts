// The chart palette, defined once for light and dark.
// Teal is a measurement, amber a trust warning, red an invalid result; the
// percentile ramp darkens toward the tail, so p99 reads as the heaviest line.
export interface ChartPalette {
  ink: string;
  muted: string;
  grid: string;
  surface: string;
  band: string;
  bandAlternate: string;
  bandExcluded: string;
  measure: string;
  offered: string;
  warning: string;
  invalid: string;
  percentiles: { p50: string; p95: string; p99: string };
}

export const lightPalette: ChartPalette = {
  ink: "#1d2433",
  muted: "#5b6576",
  grid: "#e3e7ee",
  surface: "#ffffff",
  band: "rgba(15, 124, 140, 0.05)",
  bandAlternate: "rgba(15, 124, 140, 0.13)",
  bandExcluded: "rgba(180, 35, 24, 0.10)",
  measure: "#0f7c8c",
  offered: "#8a94a6",
  warning: "#b7791f",
  invalid: "#b42318",
  percentiles: { p50: "#7cc4cc", p95: "#0f7c8c", p99: "#0a4a54" },
};

export const darkPalette: ChartPalette = {
  ink: "#e6eaf0",
  muted: "#9aa4b5",
  grid: "#2a3140",
  surface: "#1a202b",
  band: "rgba(94, 196, 208, 0.05)",
  bandAlternate: "rgba(94, 196, 208, 0.14)",
  bandExcluded: "rgba(240, 100, 90, 0.14)",
  measure: "#5ec4d0",
  offered: "#7b8496",
  warning: "#e0a84a",
  invalid: "#f0645a",
  percentiles: { p50: "#2e7d87", p95: "#5ec4d0", p99: "#b8eef3" },
};
