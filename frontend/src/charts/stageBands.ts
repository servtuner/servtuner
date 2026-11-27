import type { ClientTimeseries } from "../api/types";
import type { ChartPalette } from "./palette";

type StageBand = ClientTimeseries["stages"][number];

/** Space above the plot for the band labels; charts with bands use it as grid.top. */
export const STAGE_LABEL_SPACE = 44;

/**
 * The stage bands every time chart carries: one shaded
 * area per stage, alternating in depth so neighbours stay apart, labelled
 * above the plot with the stage's offered rate. An excluded stage is tinted
 * red and hatched, and an invalid one says so in its label, so a number read
 * off an untrustworthy stage is never unmarked.
 */
export function stageBandsMarkArea(stages: StageBand[], palette: ChartPalette) {
  return {
    silent: false,
    data: stages.map((stage, index) => [
      {
        name: bandLabel(stage),
        xAxis: stage.start_offset_seconds,
        itemStyle: stage.is_excluded
          ? {
              color: palette.bandExcluded,
              decal: {
                symbol: "rect",
                dashArrayX: [1, 0],
                dashArrayY: [2, 5],
                rotation: Math.PI / 4,
                color: palette.bandExcluded,
              },
            }
          : { color: index % 2 === 0 ? palette.band : palette.bandAlternate },
        label: {
          show: true,
          position: "top",
          distance: 4,
          color: stage.is_excluded ? palette.invalid : palette.muted,
          fontSize: 11,
        },
      },
      { xAxis: stage.end_offset_seconds },
    ]),
  };
}

export function bandLabel(stage: StageBand): string {
  const rate = `Stage ${stage.stage_index}, ${formatRate(stage.offered_rps)} RPS`;
  if (stage.is_excluded) return `${rate} (excluded)`;
  if (!stage.is_valid) return `${rate} (invalid)`;
  return rate;
}

function formatRate(rps: number): string {
  return Number.isInteger(rps) ? String(rps) : rps.toFixed(1);
}
