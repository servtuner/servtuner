import { Alert, Anchor, Badge, Button, Group, Loader, Stack, Switch, Table, Text, Title } from "@mantine/core";
import { Link, useNavigate, useParams } from "react-router";
import { useSchedule, useScheduleActions } from "../../api/hooks";
import type { ScheduleDetail } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { StatusBadge } from "../../components/StatusBadge";
import { TimeValue } from "../../components/TimeValue";
import { formatNumber, formatPercent } from "../../components/format";
import { VERDICT_META, describeInterval } from "./verdicts";

export function SchedulePage() {
  const { scheduleId = "" } = useParams();
  const schedule = useSchedule(scheduleId);
  if (schedule.isPending) return <Loader aria-label="Loading the schedule" />;
  if (schedule.isError) return <QueryError title="Could not load the schedule" error={schedule.error} />;
  return <Schedule schedule={schedule.data} />;
}

function Schedule({ schedule }: { schedule: ScheduleDetail }) {
  const navigate = useNavigate();
  const { update, trigger, remove } = useScheduleActions(schedule.id);
  const latest = schedule.runs.at(-1);
  const isBusy = latest?.verdict === "pending";
  const regressions = schedule.runs.filter((run) => run.verdict === "regressed").length;
  return (
    <Stack>
      <Group justify="space-between" align="start">
        <Stack gap={4}>
          <Title order={2}>{schedule.name}</Title>
          <Text size="sm" c="dimmed">
            Runs {describeInterval(schedule.every_hours)}; next <TimeValue timestamp={schedule.next_run_at} />. A drop
            of more than {formatPercent(schedule.regression_tolerance)} in SLO-max RPS against the baseline is a
            regression.
          </Text>
        </Stack>
        <Group>
          <Switch
            label="Enabled"
            checked={schedule.is_enabled}
            disabled={update.isPending}
            onChange={(event) => update.mutate({ is_enabled: event.currentTarget.checked })}
          />
          <Button variant="default" onClick={() => trigger.mutate()} loading={trigger.isPending} disabled={isBusy}>
            Run now
          </Button>
          <Button
            variant="subtle"
            color="red"
            loading={remove.isPending}
            onClick={() =>
              remove.mutate(undefined, {
                onSuccess: () => navigate("/schedules"),
              })
            }
          >
            Delete
          </Button>
        </Group>
      </Group>
      {latest?.verdict === "regressed" && (
        <Alert color="red" title="The latest run regressed">
          Its SLO-max is {formatNumber(latest.slo_max_rps)} RPS, {formatPercent(-(latest.change ?? 0))} below the
          baseline's {formatNumber(schedule.baseline_slo_max_rps)} RPS. Compare the two runs' Server tabs for the cause.
        </Alert>
      )}
      {schedule.last_note && (
        <Alert color="yellow" variant="light">
          {schedule.last_note}
        </Alert>
      )}
      {trigger.isError && <QueryError title="Could not start a run" error={trigger.error} />}
      {update.isError && <QueryError title="Could not change the schedule" error={update.error} />}
      {schedule.runs.length === 0 ? (
        <Text c="dimmed">No runs yet: the first is due at the next run time, or start one with Run now.</Text>
      ) : (
        <>
          <Text size="sm">
            {schedule.runs.length} run(s); {regressions === 0 ? "no regressions" : `${regressions} regressed`}.{" "}
            {schedule.effective_baseline_run_id
              ? schedule.baseline_run_id
                ? "The baseline was chosen by hand."
                : "The baseline is the first run with an SLO-max."
              : "No run has an SLO-max yet, so there is no baseline."}
          </Text>
          <Table striped verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Run</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th>Started</Table.Th>
                <Table.Th>SLO-max RPS</Table.Th>
                <Table.Th>Against baseline</Table.Th>
                <Table.Th>Verdict</Table.Th>
                <Table.Th />
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {schedule.runs.map((run) => {
                const meta = VERDICT_META[run.verdict];
                return (
                  <Table.Tr key={run.run_id}>
                    <Table.Td>
                      <Anchor component={Link} to={`/runs/${run.run_id}`} ff="monospace" size="sm">
                        {run.run_id}
                      </Anchor>
                    </Table.Td>
                    <Table.Td>
                      <StatusBadge status={run.status} />
                    </Table.Td>
                    <Table.Td>
                      <TimeValue timestamp={run.created_at} />
                    </Table.Td>
                    <Table.Td>{formatNumber(run.slo_max_rps)}</Table.Td>
                    <Table.Td>
                      {run.change == null ? "–" : `${run.change > 0 ? "+" : ""}${formatPercent(run.change)}`}
                    </Table.Td>
                    <Table.Td>
                      <Badge color={meta.color} variant="light">
                        {meta.label}
                      </Badge>
                    </Table.Td>
                    <Table.Td>
                      {run.slo_max_rps != null && run.verdict !== "baseline" && (
                        <Button
                          size="compact-xs"
                          variant="subtle"
                          onClick={() => update.mutate({ baseline_run_id: run.run_id })}
                        >
                          Use as baseline
                        </Button>
                      )}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
          {latest && schedule.effective_baseline_run_id && latest.run_id !== schedule.effective_baseline_run_id && (
            <Anchor
              component={Link}
              size="sm"
              to={`/compare?runs=${encodeURIComponent(`${schedule.effective_baseline_run_id},${latest.run_id}`)}`}
            >
              Compare the latest run with the baseline
            </Anchor>
          )}
        </>
      )}
    </Stack>
  );
}
