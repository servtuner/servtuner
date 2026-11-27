import { Button, Group, Loader, Stack, Title } from "@mantine/core";
import { useCallback } from "react";
import { Link, useSearchParams } from "react-router";
import { useRuns } from "../../api/hooks";
import type { ListRunsQuery } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { RunsFilters } from "./RunsFilters";
import { RunsTable } from "./RunsTable";
import { Allowed } from "../auth/Allowed";

// The filter names are the API's query parameters, so the URL is shareable.
const FILTER_NAMES = ["experiment", "target_type", "status"] as const;

function filtersFromSearchParams(searchParams: URLSearchParams): ListRunsQuery {
  const filters: Record<string, string> = {};
  for (const name of FILTER_NAMES) {
    const value = searchParams.get(name);
    if (value) filters[name] = value;
  }
  return filters as ListRunsQuery;
}

export function RunsListPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const filters = filtersFromSearchParams(searchParams);
  const runs = useRuns(filters);

  const changeFilters = useCallback(
    (next: ListRunsQuery) => {
      const params = new URLSearchParams();
      for (const name of FILTER_NAMES) {
        const value = next[name];
        if (value) params.set(name, value);
      }
      setSearchParams(params, { replace: true });
    },
    [setSearchParams],
  );

  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Runs</Title>
        <Allowed action="runs.write">
          <Button component={Link} to="/runs/new" size="sm">
            New run
          </Button>
        </Allowed>
      </Group>
      <RunsFilters filters={filters} onChange={changeFilters} />
      {runs.isPending && <Loader aria-label="Loading runs" />}
      {runs.isError && <QueryError title="Could not load runs" error={runs.error} />}
      {runs.isSuccess && <RunsTable runs={runs.data} hasFilters={Object.keys(filters).length > 0} />}
    </Stack>
  );
}
