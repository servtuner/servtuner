import { Group, NativeSelect } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { useEffect, useState } from "react";
import { RUN_STATUS_META, RUN_STATUSES, TARGET_TYPE_LABELS, TARGET_TYPES } from "../../api/runStatus";
import type { ListRunsQuery, RunStatus, TargetType } from "../../api/types";
import { ExperimentInput } from "../../components/ExperimentInput";

const EXPERIMENT_DEBOUNCE_MILLISECONDS = 300;
const ANY = "";

interface RunsFiltersProps {
  filters: ListRunsQuery;
  onChange: (filters: ListRunsQuery) => void;
}

export function RunsFilters({ filters, onChange }: RunsFiltersProps) {
  const [experimentInput, setExperimentInput] = useState(filters.experiment ?? "");
  const [debouncedExperiment] = useDebouncedValue(experimentInput, EXPERIMENT_DEBOUNCE_MILLISECONDS);

  useEffect(() => {
    const experiment = debouncedExperiment.trim() || undefined;
    if (experiment !== filters.experiment) onChange({ ...filters, experiment });
  }, [debouncedExperiment, filters, onChange]);

  return (
    <Group align="flex-end">
      <ExperimentInput value={experimentInput} onChange={setExperimentInput} />
      <NativeSelect
        label="Target type"
        value={filters.target_type ?? ANY}
        data={[
          { value: ANY, label: "Any" },
          ...TARGET_TYPES.map((type) => ({ value: type, label: TARGET_TYPE_LABELS[type] })),
        ]}
        onChange={(event) =>
          onChange({ ...filters, target_type: (event.currentTarget.value || undefined) as TargetType | undefined })
        }
      />
      <NativeSelect
        label="Status"
        value={filters.status ?? ANY}
        data={[
          { value: ANY, label: "Any" },
          ...RUN_STATUSES.map((status) => ({ value: status, label: RUN_STATUS_META[status].label })),
        ]}
        onChange={(event) =>
          onChange({ ...filters, status: (event.currentTarget.value || undefined) as RunStatus | undefined })
        }
      />
    </Group>
  );
}
