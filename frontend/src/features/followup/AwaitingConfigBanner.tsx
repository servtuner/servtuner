import { Alert, Button, Code, Group, Text } from "@mantine/core";
import { useConfirmApplied } from "../../api/hooks";
import type { RunDetail } from "../../api/types";

/** On a follow-up run's page: what it waits for, and the button that starts it. */
export function AwaitingConfigBanner({ run }: { run: RunDetail }) {
  const confirm = useConfirmApplied();
  if (run.status !== "awaiting_config") return null;
  const knobs = Object.entries(run.knobs)
    .map(([name, value]) => `${name}=${String(value)}`)
    .join(", ");
  return (
    <Alert color="yellow" title="Waiting for the change this run tests">
      <Group justify="space-between" align="center">
        <Text size="sm">
          Apply <Code>{knobs || "the change"}</Code> to the target, then start the run. Bench checks the knob against
          the target when it runs.
        </Text>
        <Button size="xs" loading={confirm.isPending} onClick={() => confirm.mutate([run.id])}>
          I've applied it
        </Button>
      </Group>
      {confirm.isError && (
        <Text size="sm" c="red" role="alert">
          {confirm.error.message}
        </Text>
      )}
    </Alert>
  );
}
