import { Loader } from "@mantine/core";
import { lazy, Suspense } from "react";

// The experiment view draws a chart, so it loads ECharts only when opened.
const ExperimentPage = lazy(() => import("./ExperimentPage").then((module) => ({ default: module.ExperimentPage })));

export function LazyExperimentPage() {
  return (
    <Suspense fallback={<Loader aria-label="Loading experiment" />}>
      <ExperimentPage />
    </Suspense>
  );
}
