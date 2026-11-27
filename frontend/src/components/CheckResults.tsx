import { Alert, Group, List, Text, ThemeIcon } from "@mantine/core";
import type { TargetCheckView } from "../api/types";
import { formatSeconds } from "./format";
import { TimeValue } from "./TimeValue";

type CheckResult = TargetCheckView["result"];
type Step = CheckResult["checks"][number];

const STEP_LABELS: Record<Step["name"], string> = {
  resolve: "Resolve the host",
  connect: "Connect",
  tls: "Transport security",
  auth: "API key",
  model: "Model served",
  generate: "One test request",
  metrics: "Metrics in Prometheus",
  gpu_metrics: "GPU metrics (DCGM)",
  workload: "Kubernetes workload",
};

const STEP_STYLE: Record<Step["status"], { color: string; mark: string; label: string }> = {
  ok: { color: "teal", mark: "✓", label: "passed" },
  warning: { color: "yellow", mark: "!", label: "warning" },
  failed: { color: "red", mark: "✕", label: "failed" },
  skipped: { color: "gray", mark: "–", label: "skipped" },
};

const VERDICT: Record<CheckResult["status"], { color: string; title: string }> = {
  ok: { color: "teal", title: "servtuner can reach and use this target" },
  warning: { color: "yellow", title: "Reachable, with warnings" },
  failed: { color: "red", title: "servtuner cannot use this target yet" },
};

/** The steps of a Check connection, the first failure to fix first. */
export function CheckResults({ view }: { view: TargetCheckView }) {
  const { result } = view;
  const verdict = VERDICT[result.status];
  return (
    <Alert color={verdict.color} title={verdict.title} aria-label="Check result">
      <List spacing={6} size="sm" listStyleType="none" pl={0}>
        {result.checks.map((step) => {
          const style = STEP_STYLE[step.status];
          return (
            <List.Item key={step.name}>
              <Group gap="xs" wrap="nowrap" align="start">
                <ThemeIcon color={style.color} variant="light" size="sm" radius="xl" aria-label={style.label}>
                  {style.mark}
                </ThemeIcon>
                <div>
                  <Text size="sm" fw={500} span>
                    {STEP_LABELS[step.name]}
                  </Text>
                  {step.duration_seconds > 0 && (
                    <Text size="xs" c="dimmed" span>
                      {" "}
                      · {formatSeconds(step.duration_seconds)}
                    </Text>
                  )}
                  <Text size="sm" c={step.status === "skipped" ? "dimmed" : undefined}>
                    {step.message}
                  </Text>
                </div>
              </Group>
            </List.Item>
          );
        })}
      </List>
      <Text size="xs" c="dimmed" mt="sm">
        Checked <TimeValue timestamp={result.checked_at} /> from {view.checked_from}.
      </Text>
    </Alert>
  );
}
