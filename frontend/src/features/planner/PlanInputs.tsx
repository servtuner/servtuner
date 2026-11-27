import { MultiSelect, NumberInput, SimpleGrid, Stack, Switch, Text, TextInput, Title } from "@mantine/core";
import { useRuns } from "../../api/hooks";
import type { PlanRequest } from "../../api/types";
import { FIELD_GRID_STYLE } from "../../components/layout";

type Slos = PlanRequest["slos"];
type DurationSlo = Exclude<keyof Slos, "max_error_rate">;

const SLO_FIELDS: { key: DurationSlo; label: string; placeholder: string }[] = [
  { key: "ttft_p95", label: "TTFT p95", placeholder: "500ms" },
  { key: "ttft_p99", label: "TTFT p99", placeholder: "1s" },
  { key: "e2e_p95", label: "E2E p95", placeholder: "2s" },
  { key: "e2e_p99", label: "E2E p99", placeholder: "3s" },
  { key: "itl_p95", label: "ITL p95", placeholder: "50ms" },
];

// The run picker lists this many recent succeeded runs.
const RUN_CHOICES = 200;

/** The planner's left panel: runs, traffic, SLOs, availability and headroom. */
export function PlanInputs({
  request,
  onRequest,
  runIds,
  onRunIds,
}: {
  request: PlanRequest;
  onRequest: (request: PlanRequest) => void;
  runIds: string[];
  onRunIds: (runIds: string[]) => void;
}) {
  const runs = useRuns({ status: "succeeded", limit: RUN_CHOICES });
  const choices = (runs.data ?? []).map((run) => ({
    value: run.id,
    label: `${run.name} (…${run.id.slice(-6)})`,
  }));
  // A run named in the URL or a saved scenario stays selectable even when it is not listed.
  for (const id of runIds) if (!choices.some((choice) => choice.value === id)) choices.push({ value: id, label: id });
  const set = (patch: Partial<PlanRequest>) => onRequest({ ...request, ...patch });
  const setSlo = (key: DurationSlo, value: string) => {
    const slos: Slos = { ...request.slos };
    if (value.trim()) slos[key] = value.trim();
    else delete slos[key];
    set({ slos });
  };
  // The schema defaults, so each field can be changed on its own.
  const availability = { min_replicas: 2, spare: false, zones: 1, ...request.availability };
  return (
    <Stack gap="md">
      <MultiSelect
        label="Configurations"
        description="Analysed runs; each becomes a tier, or an alternative for the same tier"
        data={choices}
        value={runIds}
        onChange={onRunIds}
        searchable
      />
      <Title order={5}>Traffic</Title>
      <SimpleGrid cols={2} style={FIELD_GRID_STYLE}>
        <NumberInput
          label="Peak RPS"
          min={0}
          value={request.peak_rps}
          onChange={(value) => set({ peak_rps: Number(value) || 0 })}
        />
        <NumberInput
          label="Baseline RPS"
          description="Optional, sets the HPA minimum"
          min={0}
          value={request.baseline_rps ?? ""}
          onChange={(value) => set({ baseline_rps: value === "" ? undefined : Number(value) })}
        />
      </SimpleGrid>
      <Title order={5}>SLOs</Title>
      <SimpleGrid cols={2} style={FIELD_GRID_STYLE}>
        {SLO_FIELDS.map((field) => (
          <TextInput
            key={field.key}
            label={field.label}
            placeholder={field.placeholder}
            value={request.slos?.[field.key] ?? ""}
            onChange={(e) => setSlo(field.key, e.currentTarget.value)}
          />
        ))}
      </SimpleGrid>
      <Text size="xs" c="dimmed">
        Capacity is recomputed for these SLOs from the measured curves, interpolating only between measured stages.
      </Text>
      <Title order={5}>Availability and headroom</Title>
      <SimpleGrid cols={2} style={FIELD_GRID_STYLE}>
        <NumberInput
          label="Minimum replicas"
          min={1}
          value={availability.min_replicas ?? 2}
          onChange={(value) => set({ availability: { ...availability, min_replicas: Number(value) || 1 } })}
        />
        <NumberInput
          label="Zones"
          description="Replicas round up to a multiple"
          min={1}
          value={availability.zones ?? 1}
          onChange={(value) => set({ availability: { ...availability, zones: Number(value) || 1 } })}
        />
        <NumberInput
          label="Replica headroom"
          description="Spare capacity per replica"
          min={0}
          max={90}
          suffix="%"
          placeholder="20% (default)"
          value={request.replica_headroom == null ? "" : Math.round(request.replica_headroom * 100)}
          onChange={(value) => set({ replica_headroom: value === "" ? undefined : Number(value) / 100 })}
        />
        <NumberInput
          label="Burst factor"
          min={1}
          step={0.1}
          decimalScale={2}
          placeholder="1 (default)"
          value={request.burst_factor ?? ""}
          onChange={(value) => set({ burst_factor: value === "" ? undefined : Number(value) })}
        />
      </SimpleGrid>
      <Switch
        label="N+1: one replica beyond the peak"
        checked={availability.spare ?? false}
        onChange={(e) => set({ availability: { ...availability, spare: e.currentTarget.checked } })}
      />
    </Stack>
  );
}
