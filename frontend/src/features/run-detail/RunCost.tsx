import { Anchor, Text } from "@mantine/core";
import { Link } from "react-router";
import { useRunDocument } from "../../api/hooks";
import type { RunStatus } from "../../api/types";
import { costLines } from "./cost";

/** What bench priced the run at (capacity.json), or why it is unpriced: never $0. */
export function RunCost({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const capacity = useRunDocument("getCapacity", runId, status);
  if (!capacity.data) return <>–</>;
  const lines = costLines(capacity.data);
  return (
    <>
      {lines.map(({ text, hover }) => (
        <Text key={text} size="sm" span display="block" title={hover}>
          {text}
        </Text>
      ))}
      <Anchor component={Link} to={`/runs/${runId}?tab=capacity`} size="xs">
        Capacity
      </Anchor>
    </>
  );
}
