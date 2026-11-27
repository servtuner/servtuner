import { describe, expect, it } from "vitest";
import type { ManagedTarget } from "../../api/types";
import { managedTarget } from "../../test/fixtures";
import { deploymentState } from "./summary";

const deployment = { knobs: {}, args: [], deployed_at: "2026-11-25T09:00:00Z" };

describe("deployment state", () => {
  it("reads the last thing servtuner did to the target", () => {
    expect(deploymentState(managedTarget)).toEqual({ label: "Not deployed", color: "gray", since: null });
    expect(deploymentState({ ...managedTarget, deployment })).toMatchObject({
      label: "Deployed",
      since: "2026-11-25T09:00:00Z",
    });
    const freed = { ...managedTarget, deployment: { ...deployment, scaled_down_at: "2026-11-25T10:00:00Z" } };
    expect(deploymentState(freed)).toMatchObject({ label: "Scaled to zero", since: "2026-11-25T10:00:00Z" });
  });

  it("says Stopped for a host's vLLM", () => {
    const host = {
      spec: { placement: "host" } as ManagedTarget["spec"],
      deployment: { ...deployment, scaled_down_at: "2026-11-25T10:00:00Z" },
    };
    expect(deploymentState(host).label).toBe("Stopped");
  });
});
