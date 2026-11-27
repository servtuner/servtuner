import {
  Alert,
  Button,
  Code,
  CopyButton,
  Group,
  List,
  Modal,
  NumberInput,
  Stack,
  Text,
  TextInput,
} from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import { useConfirmApplied, useCreateFollowup, useRun } from "../../api/hooks";
import type { Followup, Recommendations } from "../../api/types";

type Recommendation = Recommendations["recommendations"][number];

/**
 * The follow-up flow. servtuner does not deploy targets,
 * so it creates runs with the new knob value that wait until you apply the
 * change and confirm; bench then checks the declared knob against the target.
 */
export function FollowupButton({ runId, recommendation }: { runId: string; recommendation: Recommendation }) {
  const [opened, setOpened] = useState(false);
  return (
    <>
      <Button size="xs" variant="light" onClick={() => setOpened(true)}>
        Confirm with an experiment
      </Button>
      <Modal opened={opened} onClose={() => setOpened(false)} title="Confirm with an experiment" size="lg">
        <FollowupSteps runId={runId} recommendation={recommendation} />
      </Modal>
    </>
  );
}

function FollowupSteps({ runId, recommendation }: { runId: string; recommendation: Recommendation }) {
  const source = useRun(runId);
  if (source.isPending) return null;
  return (
    <FollowupForm runId={runId} recommendation={recommendation} sourceExperiment={source.data?.experiment ?? ""} />
  );
}

function FollowupForm({
  runId,
  recommendation,
  sourceExperiment,
}: {
  runId: string;
  recommendation: Recommendation;
  sourceExperiment: string;
}) {
  const [repeats, setRepeats] = useState(3);
  // This run's experiment when it has one; empty lets the API name it <run>-<knob>.
  const [experiment, setExperiment] = useState(sourceExperiment);
  const create = useCreateFollowup(runId);
  const confirm = useConfirmApplied();
  const followup = create.data;
  const change = `${recommendation.subject.name} = ${String(recommendation.recommended)}`;
  if (!followup) {
    return (
      <Stack>
        <Text size="sm">
          servtuner creates runs identical to this one except for <Code>{change}</Code>, all in one experiment, so
          comparing them with this run's configuration tells you whether the change helps. They wait until you apply the
          change to the target.
        </Text>
        <TextInput
          label="Experiment"
          description={
            sourceExperiment
              ? "The runs join this run's experiment unless you name another."
              : "This run has no experiment. Leave it empty and servtuner names one after the run and the knob."
          }
          placeholder="named after this run and the knob"
          value={experiment}
          onChange={(event) => setExperiment(event.currentTarget.value)}
        />
        <NumberInput
          label="Repeats"
          description="Three or more give an interval to judge the difference by"
          min={1}
          max={10}
          w={200}
          value={repeats}
          onChange={(value) => setRepeats(typeof value === "number" ? value : 3)}
        />
        {create.isError && (
          <Text size="sm" c="red" role="alert">
            {create.error.message}
          </Text>
        )}
        <Group justify="flex-end">
          <Button
            loading={create.isPending}
            onClick={() =>
              create.mutate({
                recommendation_id: recommendation.id,
                repeats,
                ...(experiment.trim() ? { experiment: experiment.trim() } : {}),
              })
            }
          >
            Create {repeats} {repeats === 1 ? "run" : "runs"}
          </Button>
        </Group>
      </Stack>
    );
  }
  return <ApplyAndConfirm followup={followup} confirm={confirm} />;
}

function ApplyAndConfirm({ followup, confirm }: { followup: Followup; confirm: ReturnType<typeof useConfirmApplied> }) {
  const ids = followup.runs.map((run) => run.id);
  const experiment = followup.runs[0]?.experiment;
  if (confirm.isSuccess) {
    return (
      <Alert color="teal" title="Runs launched">
        The {ids.length === 1 ? "run is" : `${ids.length} runs are`} running with{" "}
        <Code>
          {followup.knob} = {String(followup.value)}
        </Code>
        . After they finish, servtuner checks the knob against the target.
        {experiment && (
          <>
            {" "}
            Compare them on the <Link to={`/experiments/${encodeURIComponent(experiment)}`}>experiment page</Link>.
          </>
        )}
      </Alert>
    );
  }
  return (
    <Stack>
      {experiment && (
        <Text size="sm" c="dimmed">
          {ids.length === 1 ? "The run is" : `The ${ids.length} runs are`} in experiment{" "}
          <Link to={`/experiments/${encodeURIComponent(experiment)}`}>{experiment}</Link>.
        </Text>
      )}
      <Text size="sm">
        1. Apply the change to the target:{" "}
        <Code>
          {followup.knob} = {String(followup.value)}
        </Code>
      </Text>
      {followup.patch && (
        <Stack gap={4}>
          <Group justify="space-between">
            <Text size="xs" c="dimmed">
              The patch from this run's analysis
            </Text>
            <CopyButton value={followup.patch}>
              {({ copied, copy }) => (
                <Button size="compact-xs" variant="subtle" onClick={copy}>
                  {copied ? "Copied" : "Copy patch"}
                </Button>
              )}
            </CopyButton>
          </Group>
          <Code block>{followup.patch}</Code>
        </Stack>
      )}
      <Text size="sm">2. Confirm, and these runs start:</Text>
      <List size="sm">
        {followup.runs.map((run) => (
          <List.Item key={run.id}>
            <Link to={`/runs/${run.id}`}>{run.name}</Link>
          </List.Item>
        ))}
      </List>
      {confirm.isError && (
        <Text size="sm" c="red" role="alert">
          {confirm.error.message}
        </Text>
      )}
      <Group justify="flex-end">
        <Button loading={confirm.isPending} onClick={() => confirm.mutate(ids)}>
          I've applied it
        </Button>
      </Group>
    </Stack>
  );
}
