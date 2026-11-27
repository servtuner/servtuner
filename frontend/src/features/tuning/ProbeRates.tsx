import { Anchor, Button, Group, Select, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import { useRuns } from "../../api/hooks";
import type { ManagedTarget, WorkloadPreset } from "../../api/types";
import { formatNumber } from "../../components/format";
import { runLabelFilter } from "../managed-targets/summary";
import { hintProbeRates, measuredProbeRates } from "./spaces";

/**
 * Where the session's probe ladder starts: the target's newest SLO-max for this preset, from
 * its own runs (same hardware and model), else the preset's hand-written hint for a GPU class. The
 * source is always shown; applying sets ladder.probe_rps in the space.
 */
export function ProbeRates({
  target,
  preset,
  onApply,
}: {
  target: ManagedTarget;
  preset: WorkloadPreset | undefined;
  onApply: (rates: number[]) => void;
}) {
  const runs = useRuns({ label: runLabelFilter(target) });
  const [gpuClass, setGpuClass] = useState<string | null>(null);
  if (!preset) return null;
  const reference = `${preset.name}@${preset.version}`;
  // Newest first: the latest measurement of this target with this request mix.
  const measured = runs.data?.find((run) => run.workload_preset === reference && run.slo_max_rps != null);
  if (measured?.slo_max_rps != null) {
    const rates = measuredProbeRates(measured.slo_max_rps);
    return (
      <Group gap="sm">
        <Text size="sm">
          Measured on {target.name}: SLO-max {formatNumber(measured.slo_max_rps, 3)} RPS with {reference} (
          <Anchor component={Link} to={`/runs/${measured.id}`}>
            {measured.name}
          </Anchor>
          ). Probe at {rates.join(", ")} RPS.
        </Text>
        <Button variant="default" size="xs" onClick={() => onApply(rates)}>
          Use these probe rates
        </Button>
      </Group>
    );
  }
  const hint = preset.rps_hint.find((candidate) => candidate.gpu_class === gpuClass);
  return (
    <Stack gap={4}>
      <Text size="sm" c="dimmed">
        No run of {target.name} measured {reference} yet. Start from the preset&apos;s hand-written hint for its GPU:
      </Text>
      <Group gap="sm" align="end">
        <Select
          aria-label="GPU class for the preset's hint"
          placeholder="GPU class"
          data={preset.rps_hint.map((candidate) => candidate.gpu_class)}
          value={gpuClass}
          onChange={setGpuClass}
          w={220}
        />
        <Button
          variant="default"
          size="xs"
          disabled={!hint}
          onClick={() => hint && onApply(hintProbeRates(hint.start_rps))}
        >
          {hint ? `Probe at ${hintProbeRates(hint.start_rps).join(", ")} RPS` : "Use the hint"}
        </Button>
      </Group>
    </Stack>
  );
}
