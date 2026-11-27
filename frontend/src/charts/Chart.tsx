import { useChart, type ChartOption } from "./useChart";

/** A chart with an accessible name; the option says everything else. */
export function Chart({ option, label, height = 280 }: { option: ChartOption; label: string; height?: number }) {
  const element = useChart(option);
  return <div ref={element} role="img" aria-label={label} style={{ width: "100%", height }} />;
}
