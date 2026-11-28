import { Anchor, Badge, Button, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { Link } from "react-router";
import { useManagedTargets, useTuningSessions } from "../../api/hooks";
import { PresetName } from "../../components/PresetName";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { ADD_HOST_FOR_TUNING, STATUS_BADGE } from "./session";

/** Tuning: sessions that search a managed server's flags. */
export function TuningPage() {
  const sessions = useTuningSessions();
  const targets = useManagedTargets();
  const targetName = new Map((targets.data ?? []).map((target) => [target.id, target.name]));
  return (
    <Stack gap="lg">
      <Group justify="space-between">
        <Title order={2}>Tuning</Title>
        <Group gap="xs">
          <Button component={Link} variant="default" to={ADD_HOST_FOR_TUNING}>
            Add a vLLM host
          </Button>
          <Button component={Link} to="/tuning/new" disabled={!targets.data?.length}>
            New session
          </Button>
        </Group>
      </Group>
      <Text c="dimmed" maw={720} size="sm">
        A session finds the best vLLM command line for one workload preset and set of SLOs: servtuner deploys a managed
        vLLM, boots each candidate, benchmarks the promising ones, and ends with a recommended command line. One session
        runs at a time, because sessions share the GPU they measure.
      </Text>
      <Title order={4}>Sessions</Title>
      {sessions.isPending && <Loader size="sm" aria-label="Loading sessions" />}
      {sessions.isError && <QueryError title="Could not load sessions" error={sessions.error} />}
      {sessions.isSuccess && sessions.data.length === 0 && (
        <Text size="sm" c="dimmed">
          No sessions yet.
        </Text>
      )}
      {sessions.isSuccess && sessions.data.length > 0 && (
        <Table striped verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Session</Table.Th>
              <Table.Th>Target</Table.Th>
              <Table.Th>Workload</Table.Th>
              <Table.Th>Runs</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>Started</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {sessions.data.map((session) => (
              <Table.Tr key={session.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/tuning/${session.id}`}>
                    {session.experiment}
                  </Anchor>
                </Table.Td>
                <Table.Td>{targetName.get(session.managed_target_id) ?? session.managed_target_id}</Table.Td>
                <Table.Td>
                  <PresetName preset={session.request.preset} />
                </Table.Td>
                <Table.Td>{session.runs_started}</Table.Td>
                <Table.Td>
                  <Badge variant="light" color={STATUS_BADGE[session.status].color}>
                    {STATUS_BADGE[session.status].label}
                  </Badge>
                </Table.Td>
                <Table.Td>
                  <TimeValue timestamp={session.created_at} />
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {targets.isSuccess && targets.data.length === 0 && (
        <Text size="sm">
          A session needs a managed server to deploy:{" "}
          <Anchor component={Link} to="/managed-targets">
            add one first
          </Anchor>
          .
        </Text>
      )}
    </Stack>
  );
}
