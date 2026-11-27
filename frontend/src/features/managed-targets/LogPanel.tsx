import { Button, Code, Group, Loader, ScrollArea, SegmentedControl, Stack, Text, Title } from "@mantine/core";
import { useState } from "react";
import { useManagedTargetLogs } from "../../api/hooks";
import { QueryError } from "../../components/QueryError";

// The api's default window (the boot classifier's), and its most (servtuner launcher's MaxLogTail).
const TAIL_CHOICES = ["200", "1000"];
const LOG_HEIGHT_PX = 420; // about 25 lines before the box scrolls

/** The end of vLLM's log: why a boot failed, or what a running vLLM is doing, without kubectl or SSH. */
export function LogPanel({ targetId, isBooting }: { targetId: string; isBooting: boolean }) {
  const [tailLines, setTailLines] = useState(TAIL_CHOICES[0]!);
  const log = useManagedTargetLogs(targetId, Number(tailLines), isBooting);
  return (
    <Stack gap="xs">
      <Group justify="space-between" maw={900}>
        <Title order={4}>vLLM log</Title>
        <Group gap="xs">
          <SegmentedControl
            size="xs"
            value={tailLines}
            onChange={setTailLines}
            data={TAIL_CHOICES.map((value) => ({ value, label: `Last ${value} lines` }))}
          />
          <Button variant="default" size="xs" loading={log.isFetching} onClick={() => log.refetch()}>
            Refresh
          </Button>
        </Group>
      </Group>
      {log.isPending && <Loader size="sm" aria-label="Loading the vLLM log" />}
      {log.isError && <QueryError title="Could not read the vLLM log" error={log.error} />}
      {log.isSuccess && (
        <>
          <Text size="xs" c="dimmed">
            From {log.data.source}
            {isBooting && ", following the boot"}
          </Text>
          {log.data.lines.length > 0 ? (
            <ScrollArea.Autosize mah={LOG_HEIGHT_PX} maw={900}>
              <Code block aria-label="vLLM log">
                {log.data.lines.join("\n")}
              </Code>
            </ScrollArea.Autosize>
          ) : (
            <Text size="sm" c="dimmed">
              No log lines yet.
            </Text>
          )}
        </>
      )}
    </Stack>
  );
}
