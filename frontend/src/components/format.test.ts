import { describe, expect, it } from "vitest";
import { formatBytes, formatHertz, formatUsd, scaleBytes } from "./format";

describe("formatUsd", () => {
  it("shows cents from a dollar up and three significant figures below", () => {
    expect(formatUsd(4.2)).toBe("$4.20");
    expect(formatUsd(1234.5)).toBe("$1,234.50");
    expect(formatUsd(0.0178125)).toBe("$0.0178");
    expect(formatUsd(0.00020744)).toBe("$0.000207");
  });

  it("says nothing rather than zero when there is no price", () => {
    expect(formatUsd(null)).toBe("–");
    expect(formatUsd(undefined)).toBe("–");
  });
});

describe("formatBytes", () => {
  it("moves to the next binary unit at 1024", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1 KiB");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(1024 * 1024 - 1)).toBe("1024 KiB");
    expect(formatBytes(2 * 1024 ** 3)).toBe("2 GiB");
    expect(formatBytes(2048 * 1024 ** 4)).toBe("2048 TiB"); // TiB is the largest unit
  });

  it("says nothing rather than zero when there is no value", () => {
    expect(formatBytes(null)).toBe("–");
    expect(formatBytes(undefined)).toBe("–");
  });

  it("keeps the number and unit apart for styling", () => {
    expect(scaleBytes(1536)).toEqual({ value: "1.5", unit: "KiB" });
  });
});

describe("formatHertz", () => {
  it("shows MHz below a gigahertz and GHz from there", () => {
    expect(formatHertz(810e6)).toBe("810 MHz");
    expect(formatHertz(1.905e9)).toBe("1.91 GHz");
    expect(formatHertz(null)).toBe("–");
  });
});
