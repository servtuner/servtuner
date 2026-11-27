import type { ManagedTarget } from "../../api/types";

/** Where a managed vLLM runs, in one line: a Kubernetes namespace or a host's launcher. */
export function placementLine(target: Pick<ManagedTarget, "spec" | "namespace">): string {
  return target.spec.placement === "host"
    ? `Host, launcher ${target.spec.launcher_url}`
    : `Kubernetes, namespace ${target.namespace}`;
}

export type DeploymentState = { label: string; color: string; since: string | null };

/**
 * What servtuner last did to the target: never deployed, deployed, or freed (scaled to zero, or a
 * host's vLLM stopped). The last action, not live health: the target's page polls readiness.
 */
export function deploymentState(target: Pick<ManagedTarget, "spec" | "deployment">): DeploymentState {
  const deployment = target.deployment;
  if (!deployment) return { label: "Not deployed", color: "gray", since: null };
  if (deployment.scaled_down_at)
    return {
      label: target.spec.placement === "host" ? "Stopped" : "Scaled to zero",
      color: "gray",
      since: deployment.scaled_down_at,
    };
  return { label: "Deployed", color: "teal", since: deployment.deployed_at };
}

/** GET /runs?label= for the runs of a managed target: the one label in its run_labels. */
export function runLabelFilter(target: Pick<ManagedTarget, "run_labels">): string {
  const [label] = Object.entries(target.run_labels);
  return label ? `${label[0]}=${label[1]}` : "";
}
