/**
 * Index of the run whose interval lies wholly below every other run's, or -1.
 * The best value is highlighted only where the intervals allow it.
 */
export function separatedLowest(lows: (number | null)[], highs: (number | null)[]): number {
  let best = -1;
  for (let i = 0; i < lows.length; i++) {
    const high = highs[i];
    if (high == null) continue;
    const clear = lows.every((low, j) => j === i || (low != null && high < low));
    if (clear) best = i;
  }
  return best;
}
