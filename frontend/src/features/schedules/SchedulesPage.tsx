import {
  Alert,
  Anchor,
  Badge,
  Button,
  Group,
  Loader,
  NumberInput,
  Stack,
  Table,
  Text,
  TextInput,
  Textarea,
  Title,
} from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { parse } from "yaml";
import { useCreateSchedule, useSchedules, useSubmittedSpec } from "../../api/hooks";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { describeInterval } from "./verdicts";

const DEFAULT_EVERY_HOURS = 24;
const MIN_EVERY_HOURS = 1;
const MAX_EVERY_HOURS = 720; // thirty days, the api's limit

/** Scheduled regression runs: each re-runs one RunSpec and flags an SLO-max drop. */
export function SchedulesPage() {
  const [searchParams] = useSearchParams();
  const from = searchParams.get("from");
  const schedules = useSchedules();
  const clone = useSubmittedSpec(from);
  return (
    <Stack>
      <Title order={2}>Schedules</Title>
      <Text c="dimmed" maw={680} size="sm">
        A schedule re-runs one RunSpec on an interval and compares each run's SLO-max RPS with a baseline, so a target
        that got slower is caught. A slot is skipped while the previous run is still going.
      </Text>
      {schedules.isPending && <Loader aria-label="Loading schedules" />}
      {schedules.isError && <QueryError title="Could not load schedules" error={schedules.error} />}
      {schedules.isSuccess && schedules.data.length > 0 && (
        <Table striped verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Schedule</Table.Th>
              <Table.Th>Interval</Table.Th>
              <Table.Th>Next run</Table.Th>
              <Table.Th>State</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {schedules.data.map((schedule) => (
              <Table.Tr key={schedule.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/schedules/${schedule.id}`}>
                    {schedule.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{describeInterval(schedule.every_hours)}</Table.Td>
                <Table.Td>
                  <TimeValue timestamp={schedule.next_run_at} />
                </Table.Td>
                <Table.Td>
                  <Badge color={schedule.is_enabled ? "teal" : "gray"} variant="light">
                    {schedule.is_enabled ? "Enabled" : "Paused"}
                  </Badge>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {schedules.isSuccess && schedules.data.length === 0 && !from && (
        <Text c="dimmed" size="sm">
          No schedules yet. Open a run and choose "Schedule this spec", or paste a RunSpec below.
        </Text>
      )}
      {from && clone.isPending ? (
        <Loader size="sm" aria-label="Loading the run's spec" />
      ) : (
        <NewScheduleForm key={from ?? "new"} from={from} initialSpec={(from && clone.data) || ""} />
      )}
    </Stack>
  );
}

function NewScheduleForm({ from, initialSpec }: { from: string | null; initialSpec: string }) {
  const navigate = useNavigate();
  const create = useCreateSchedule();
  const [name, setName] = useState(from ? `regression of ${from}` : "");
  const [everyHours, setEveryHours] = useState<number>(DEFAULT_EVERY_HOURS);
  const [startAt, setStartAt] = useState("");
  const [specText, setSpecText] = useState(initialSpec);
  const [parseError, setParseError] = useState<string | null>(null);

  function submit() {
    let spec: unknown;
    try {
      spec = parse(specText);
    } catch (error) {
      setParseError(`The RunSpec does not parse: ${String(error)}`);
      return;
    }
    if (typeof spec !== "object" || spec === null || Array.isArray(spec)) {
      setParseError("The RunSpec must be a YAML or JSON object.");
      return;
    }
    setParseError(null);
    create.mutate(
      {
        name: name.trim(),
        spec: spec as Record<string, unknown>,
        every_hours: everyHours,
        start_at: startAt ? new Date(startAt).toISOString() : undefined,
      },
      { onSuccess: (schedule) => navigate(`/schedules/${schedule.id}`) },
    );
  }

  return (
    <Stack gap="sm" maw={760}>
      <Title order={3}>New schedule</Title>
      <Group align="end">
        <TextInput
          label="Name"
          required
          value={name}
          onChange={(event) => setName(event.currentTarget.value)}
          w={320}
        />
        <NumberInput
          label="Every"
          suffix=" hours"
          min={MIN_EVERY_HOURS}
          max={MAX_EVERY_HOURS}
          value={everyHours}
          onChange={(value) => setEveryHours(typeof value === "number" ? value : DEFAULT_EVERY_HOURS)}
          w={140}
        />
        <TextInput
          label="First run"
          description="Local time; empty runs it now"
          type="datetime-local"
          value={startAt}
          onChange={(event) => setStartAt(event.currentTarget.value)}
          w={240}
        />
      </Group>
      <Textarea
        label="RunSpec"
        spellCheck={false} // code-like text: no misspelling underlines
        description={from ? `From run ${from}` : "YAML or JSON, as for a new run"}
        required
        autosize
        minRows={8}
        maxRows={24}
        styles={{
          input: { fontFamily: "var(--mantine-font-family-monospace)" },
        }}
        value={specText}
        onChange={(event) => setSpecText(event.currentTarget.value)}
      />
      {parseError && <Alert color="red">{parseError}</Alert>}
      {create.isError && <QueryError title="Could not create the schedule" error={create.error} />}
      <Group>
        <Button onClick={submit} loading={create.isPending} disabled={!name.trim() || !specText.trim()}>
          Create schedule
        </Button>
      </Group>
    </Stack>
  );
}
