import { ModelName } from "../../components/ModelName";
import { PresetName } from "../../components/PresetName";
import { Anchor, Button, Checkbox, Group, Stack, Table, Text } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { targetTypeLabel } from "../../api/runStatus";
import type { RunResource } from "../../api/types";
import { StatusBadge } from "../../components/StatusBadge";
import { formatNumber } from "../../components/format";
import { TimeValue } from "../../components/TimeValue";

export function RunsTable({ runs, hasFilters }: { runs: RunResource[]; hasFilters: boolean }) {
  if (runs.length === 0) {
    return <Text c="dimmed">{hasFilters ? "No runs match these filters." : "No runs yet."}</Text>;
  }
  return <SelectableRuns runs={runs} />;
}

// The most runs one comparison lines up (the api's MAX_COMPARED_RUNS).
const MAX_COMPARED = 50;

function SelectableRuns({ runs }: { runs: RunResource[] }) {
  const navigate = useNavigate();
  const [selected, setSelected] = useState<string[]>([]);
  const toggle = (runId: string) =>
    setSelected((current) => (current.includes(runId) ? current.filter((id) => id !== runId) : [...current, runId]));
  const canCompare = selected.length >= 2 && selected.length <= MAX_COMPARED;
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          {selected.length === 0 ? "Select two or more runs to compare them." : `${selected.length} selected`}
        </Text>
        <Button
          size="xs"
          disabled={!canCompare}
          onClick={() => navigate(`/compare?runs=${selected.map(encodeURIComponent).join(",")}`)}
        >
          Compare {selected.length >= 2 ? `${selected.length} runs` : "runs"}
        </Button>
      </Group>
      <Table.ScrollContainer minWidth={640}>
        <Table highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th w={36} />
              <Table.Th>Name</Table.Th>
              <Table.Th>Model</Table.Th>
              <Table.Th>Experiment</Table.Th>
              <Table.Th>Target type</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>SLO-max (RPS)</Table.Th>
              <Table.Th>Created</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {runs.map((run) => (
              <Table.Tr key={run.id}>
                <Table.Td>
                  <Checkbox
                    aria-label={`Select ${run.name}`}
                    checked={selected.includes(run.id)}
                    onChange={() => toggle(run.id)}
                  />
                </Table.Td>
                <Table.Td>
                  <Anchor component={Link} to={`/runs/${encodeURIComponent(run.id)}`}>
                    {run.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  <Group gap={4} wrap="nowrap">
                    <ModelName model={run.model} />
                    <PresetName preset={run.workload_preset} />
                  </Group>
                </Table.Td>
                <Table.Td>{run.experiment ?? "-"}</Table.Td>
                <Table.Td>{targetTypeLabel(run.target_type)}</Table.Td>
                <Table.Td>
                  <StatusBadge status={run.status} />
                </Table.Td>
                <Table.Td>{run.slo_max_rps == null ? "–" : formatNumber(run.slo_max_rps, 3)}</Table.Td>
                <Table.Td>
                  <TimeValue timestamp={run.created_at} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </Stack>
  );
}
