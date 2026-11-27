import { describe, expect, it } from "vitest";
import { describeStep } from "./session";

describe("session helpers", () => {
  it("says what each phase is doing", () => {
    expect(describeStep(null)).toBe("Finished");
    expect(describeStep({ phase: "propose" })).toMatch(/servtuner propose/);
    expect(
      describeStep({
        phase: "booting",
        proposed: { action: "boot_probe", config_id: "c-1" },
        waiting: "loading",
      }),
    ).toBe("Starting vLLM for the boot probe of c-1: loading");
    expect(
      describeStep({
        phase: "running",
        proposed: { action: "run", config_id: "c-2" },
        repeat: 1,
      }),
    ).toBe("Running repeat 2 of c-2");
  });
});
