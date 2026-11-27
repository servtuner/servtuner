import { Anchor, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { Link } from "react-router";
import { useExperiments } from "../../api/hooks";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";

export function ExperimentsListPage() {
  const experiments = useExperiments();
  return (
    <Stack>
      <Title order={2}>Experiments</Title>
      {experiments.isPending && <Loader aria-label="Loading experiments" />}
      {experiments.isError && <QueryError title="Could not load experiments" error={experiments.error} />}
      {experiments.isSuccess && experiments.data.length === 0 && (
        <Text c="dimmed" maw={560}>
          No experiments yet. Give runs the same <code>experiment</code> name in their RunSpecs to group them here and
          compare their configurations.
        </Text>
      )}
      {experiments.isSuccess && experiments.data.length > 0 && (
        <Table striped verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Experiment</Table.Th>
              <Table.Th>Runs</Table.Th>
              <Table.Th>Latest run</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {experiments.data.map((experiment) => (
              <Table.Tr key={experiment.name}>
                <Table.Td>
                  <Anchor component={Link} to={`/experiments/${encodeURIComponent(experiment.name)}`}>
                    {experiment.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{experiment.run_count}</Table.Td>
                <Table.Td>
                  <TimeValue timestamp={experiment.latest_run_at ?? null} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
