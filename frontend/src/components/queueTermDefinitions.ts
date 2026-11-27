/** The two sizes this app recommends for a router's queue, named once for every page that uses them. */
export const QUEUE_TERMS = {
  C: { name: "concurrency", meaning: "the most requests one replica works on at the same time" },
  B: { name: "queue size", meaning: "how many more requests can wait for a free slot while all C are busy" },
} as const;

// Symbols that appear beside C and B in the derivations and caveats.
export const OTHER_SYMBOLS = [
  ["C + B", "the most requests one replica holds at once, working or waiting"],
  ["B_max", "the longest queue where the last request still meets the SLO"],
  ["B_min", "the queue needed to absorb a one-second burst"],
  ["λ", "arrival rate"],
  ["W", "time a request holds a slot"],
  ["μ", "drain rate, C / W"],
  ["ρ", "utilization, how busy the C slots are"],
] as const;

// C or B as a symbol: alone, in "C + B" or "(C)", or as B_max and B_min. Not inside a word or a
// dotted name, and not the byte unit after a number ("512 B").
const QUEUE_SYMBOL = /(?<![\w.])(?<!\d )[CB](_max|_min)?(?!\w|\.\w)/;

/** True when any of the texts uses C or B, so the page should show the key. */
export function mentionsQueueTerms(texts: (string | null | undefined)[]): boolean {
  return texts.some((text) => text != null && QUEUE_SYMBOL.test(text));
}
