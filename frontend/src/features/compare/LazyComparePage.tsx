import { Loader } from "@mantine/core";
import { lazy, Suspense } from "react";

// The compare view draws a chart, so it loads ECharts only when opened.
const ComparePage = lazy(() => import("./ComparePage").then((module) => ({ default: module.ComparePage })));

export function LazyComparePage() {
  return (
    <Suspense fallback={<Loader aria-label="Loading comparison" />}>
      <ComparePage />
    </Suspense>
  );
}
