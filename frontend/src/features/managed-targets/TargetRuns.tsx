import { Loader, Stack, Text, Title } from "@mantine/core";
import { useRuns } from "../../api/hooks";
import type { ManagedTarget } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { RunsTable } from "../runs/RunsTable";
import { runLabelFilter } from "./summary";

/** Every run that measured this managed server, its benchmarks and its sessions' runs, to compare. */
export function TargetRuns({ target }: { target: ManagedTarget }) {
  const runs = useRuns({ label: runLabelFilter(target) });
  return (
    <Stack gap="sm">
      <Title order={4}>Runs</Title>
      {runs.isPending && <Loader size="sm" aria-label="Loading the target's runs" />}
      {runs.isError && <QueryError title="Could not load the target's runs" error={runs.error} />}
      {runs.isSuccess && runs.data.length === 0 && (
        <Text size="sm" c="dimmed">
          No runs yet: Benchmark starts one, and tuning sessions add theirs.
        </Text>
      )}
      {runs.isSuccess && runs.data.length > 0 && <RunsTable runs={runs.data} hasFilters />}
    </Stack>
  );
}
