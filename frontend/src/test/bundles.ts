// Documents for the analysis tabs, taken from the files bench and the schema
// tests already use (mocks come from the golden bundles).
// A contract change breaks the build here, through the type annotations.
import histogramsJson from "../../../schema/examples/chart-histograms/valid/one-stage.json?raw";
import timeseriesJson from "../../../schema/examples/chart-client-timeseries/valid/two-stages.json?raw";
import capacityJson from "../../../schema/examples/capacity/valid/ladder-with-target.json?raw";
import pricedCapacityJson from "../../../schema/examples/capacity/valid/gpus-and-cost.json?raw";
import degradedAnalysisJson from "../../../schema/examples/analysis/valid/degraded-with-capacity.json?raw";
import recommendationsJson from "../../../schema/examples/recommendations/valid/sizing-slo-max.json?raw";
import winnerComparisonJson from "../../../schema/examples/comparison/valid/winner-with-repeats.json?raw";
import notComparableJson from "../../../schema/examples/comparison/valid/not-comparable-single-samples.json?raw";
import environmentJson from "../../../schema/examples/environment/valid/pod-namespace-knobs.json?raw";
import snapshotJson from "../../../schema/examples/target-snapshot/valid/router-with-upstream-pre.json?raw";
import inflightJson from "../../../schema/examples/chart-inflight/valid/two-stages.json?raw";
import bifrostRecommendationsJson from "../../../bench/testdata/analysis/recommendations/bifrost-concurrency-bound.json?raw";
import planJson from "../../../schema/examples/plan/valid/one-tier.json?raw";
import singleGpuSpaceJson from "../../../schema/examples/search-space-template/valid/minimal.json?raw";
import hostSpaceJson from "../../../schema/examples/search-space-template/valid/host-only.json?raw";
import serverChartJson from "../../../schema/examples/chart-server/valid/kv-and-cpu.json?raw";
import goldenAnalysisJson from "../../../bench/testdata/analysis/bifrost-concurrency-bound.json?raw";
import type {
  SearchSpaceTemplate,
  AnalysisResult,
  BundleFile,
  CapacityTable,
  ClientTimeseries,
  Comparison,
  Environment,
  InFlightChart,
  LatencyHistograms,
  Plan,
  Recommendations,
  ServerChart,
  TargetSnapshot,
} from "../api/types";

export const goldenAnalysis: AnalysisResult = JSON.parse(goldenAnalysisJson);
export const degradedAnalysis: AnalysisResult = JSON.parse(degradedAnalysisJson);
export const timeseries: ClientTimeseries = JSON.parse(timeseriesJson);
export const histograms: LatencyHistograms = JSON.parse(histogramsJson);
export const recommendations: Recommendations = JSON.parse(recommendationsJson);
export const capacity: CapacityTable = JSON.parse(capacityJson);
export const pricedCapacity: CapacityTable = JSON.parse(pricedCapacityJson);
export const bundleFiles: BundleFile[] = [
  { path: "analysis.json", size_bytes: 20480 },
  { path: "patches/resources.yaml", size_bytes: 412 },
  { path: "requests.parquet", size_bytes: 3 * 1024 * 1024 },
];

export const winnerComparison: Comparison = JSON.parse(winnerComparisonJson);
export const notComparableComparison: Comparison = JSON.parse(notComparableJson);

export const environment: Environment = JSON.parse(environmentJson);
export const snapshot: TargetSnapshot = JSON.parse(snapshotJson);

export const inflight: InFlightChart = JSON.parse(inflightJson);
export const bifrostRecommendations: Recommendations = JSON.parse(bifrostRecommendationsJson);
export const serverChart: ServerChart = JSON.parse(serverChartJson);
export const plan: Plan = JSON.parse(planJson);

/** Two starting search spaces (New session): one GPU (Kubernetes or host), and host only. */
export const searchSpaces: SearchSpaceTemplate[] = [
  JSON.parse(singleGpuSpaceJson) as SearchSpaceTemplate,
  JSON.parse(hostSpaceJson) as SearchSpaceTemplate,
];
