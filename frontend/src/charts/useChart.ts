import { useComputedColorScheme } from "@mantine/core";
import { BarChart, LineChart, ScatterChart } from "echarts/charts";
import {
  AriaComponent,
  DataZoomComponent,
  GridComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  TooltipComponent,
} from "echarts/components";
import * as echarts from "echarts/core";
import { SVGRenderer } from "echarts/renderers";
import { useEffect, useRef } from "react";
import { darkPalette, lightPalette, type ChartPalette } from "./palette";

// Only what the charts use is registered, which keeps the bundle small. The
// SVG renderer is enough because every chart series is capped at about 2,000
// points, and it renders in jsdom, so charts are tested.
echarts.use([
  LineChart,
  BarChart,
  ScatterChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  MarkAreaComponent,
  MarkLineComponent,
  DataZoomComponent,
  AriaComponent,
  SVGRenderer,
]);

export type ChartOption = echarts.EChartsCoreOption;

/**
 * The palette of the color scheme in use. Read on the first render, not in an
 * effect: Mantine's default answers "light" first, so every chart mounted in
 * dark mode would paint light and then repaint dark.
 */
export function useChartPalette(): ChartPalette {
  return useComputedColorScheme("light", { getInitialValueInEffect: false }) === "dark" ? darkPalette : lightPalette;
}

/**
 * Renders option into the returned element and keeps it sized to it. The
 * option is replaced, not merged, on every change, so a series that goes away
 * does not linger.
 */
export function useChart(option: ChartOption) {
  const element = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (element.current === null) return;
    const instance = echarts.init(element.current, null, { renderer: "svg" });
    chart.current = instance;
    const observer = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(() => instance.resize());
    observer?.observe(element.current);
    return () => {
      observer?.disconnect();
      instance.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    chart.current?.setOption(option, { notMerge: true });
  }, [option]);

  return element;
}
