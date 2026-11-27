import { Alert, Button, Group, Select, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { useWorkloadPresets } from "../../api/hooks";
import type { WorkloadPreset } from "../../api/types";
import {
  customizeChanges,
  LADDER_FACTORS,
  LADDER_STAGE_DURATION,
  presetChanges,
  presetReference,
  startingLadder,
  type Workload,
} from "./presets";
import type { Change } from "./TargetPicker";

/** The Select value for a workload written out by hand (no preset). */
const CUSTOM = "custom";

function tokens(distribution: { mean: number; stddev?: number }): string {
  return distribution.stddev ? `${distribution.mean} ± ${distribution.stddev}` : `${distribution.mean}`;
}

function shape(preset: WorkloadPreset): string {
  const { dataset, conversation } = preset.workload;
  const parts = [`${tokens(dataset.input_tokens)} tokens in`];
  if (dataset.shared_prefix) {
    parts[0] += ` (a ${dataset.shared_prefix.tokens}-token prefix shared by ${dataset.shared_prefix.groups} groups)`;
  }
  parts.push(`${tokens(dataset.output_tokens)} out`);
  if (conversation) parts.push(`${conversation.turns} turns`);
  return `${parts.join(", ")}. Runs on ${preset.engines.join(", ")}.`;
}

export function WorkloadPicker({
  workload,
  engine,
  edit,
}: {
  workload: Workload;
  engine: string;
  edit: (changes: Change[]) => void;
}) {
  const presets = useWorkloadPresets();
  const [gpuClass, setGpuClass] = useState<string | null>(null);
  const selected = workload?.preset;
  const preset = presets.data?.find((candidate) => presetReference(candidate) === selected);
  if (presets.isError) {
    return (
      <Text size="sm" c="dimmed">
        Workload presets are unavailable ({presets.error.message}); the workload is on the YAML tab.
      </Text>
    );
  }
  const hint = preset?.rps_hint.find((candidate) => candidate.gpu_class === gpuClass);
  return (
    <Stack gap="xs">
      <Group align="end">
        <Select
          label="Workload"
          description="A preset fixes the request mix, so runs compare by name"
          data={[
            { value: CUSTOM, label: "Custom (dataset on the YAML tab)" },
            ...(presets.data ?? []).map((candidate) => ({
              value: presetReference(candidate),
              label: presetReference(candidate),
            })),
          ]}
          value={selected ?? CUSTOM}
          onChange={(value) => {
            const chosen = presets.data?.find((candidate) => presetReference(candidate) === value);
            if (chosen) edit(presetChanges(chosen, workload, engine));
          }}
          allowDeselect={false}
          disabled={presets.isPending}
          w={320}
        />
        {preset && (
          <Button variant="default" onClick={() => edit(customizeChanges(preset))}>
            Customize
          </Button>
        )}
      </Group>
      {selected && !preset && !presets.isPending && (
        <Alert color="orange" title="Unknown preset">
          {selected} is not in this servtuner&apos;s catalog; Validate will say so.
        </Alert>
      )}
      {preset && (
        <>
          <Text size="sm">{preset.description}</Text>
          <Text size="sm" c="dimmed">
            {shape(preset)}
          </Text>
          <Group align="end">
            <Select
              label="GPU class"
              description="For a starting ladder from the preset's hint"
              data={preset.rps_hint.map((candidate) => candidate.gpu_class)}
              value={gpuClass}
              onChange={setGpuClass}
              w={220}
            />
            <Button
              variant="default"
              disabled={!hint}
              onClick={() => hint && edit([[["load", "stages"], startingLadder(hint.start_rps)]])}
            >
              Use starting ladder
            </Button>
          </Group>
          {hint && (
            <Text size="xs" c="dimmed">
              {LADDER_FACTORS.map((factor) => hint.start_rps * factor).join(", ")} RPS, {LADDER_STAGE_DURATION} each: a
              hand-written starting point, not measured capacity.
            </Text>
          )}
        </>
      )}
    </Stack>
  );
}
