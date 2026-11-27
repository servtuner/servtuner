import { describe, expect, it } from "vitest";
import { mentionsQueueTerms } from "./queueTermDefinitions";

describe("mentionsQueueTerms", () => {
  it("finds C and B used as symbols", () => {
    expect(mentionsQueueTerms(["Recommended concurrency C"])).toBe(true);
    expect(mentionsQueueTerms(["Working set at C + B, with the limit's headroom"])).toBe(true);
    expect(mentionsQueueTerms(["the current queue of 100 is past B_max 78"])).toBe(true);
    expect(mentionsQueueTerms(["Random arrivals would queue at this C."])).toBe(true);
  });

  it("ignores words, dotted names and the byte unit", () => {
    expect(mentionsQueueTerms(["Bifrost overhead", "bifrost.buffer_size", "a 512 B request", "CPU p95"])).toBe(false);
    expect(mentionsQueueTerms([null, undefined])).toBe(false);
  });
});
