import { Autocomplete } from "@mantine/core";
import { useExperiments } from "../api/hooks";

interface ExperimentInputProps {
  value: string;
  onChange: (value: string) => void;
  description?: string;
}

/**
 * A free-text experiment name that suggests the experiments the API already
 * knows, so runs land in an existing group instead of a near-duplicate. A new
 * name is still allowed: typing one starts a new experiment.
 */
export function ExperimentInput({ value, onChange, description }: ExperimentInputProps) {
  const experiments = useExperiments();
  const names = experiments.data?.map((experiment) => experiment.name) ?? [];
  return (
    <Autocomplete
      label="Experiment"
      description={description}
      value={value}
      onChange={onChange}
      data={names}
      limit={20}
    />
  );
}
