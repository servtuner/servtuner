import { Table, Text } from "@mantine/core";
import type { RunDetail } from "../../api/types";

function formatSeconds(seconds: number | null | undefined): string {
  return seconds === null || seconds === undefined ? "n/a" : `${seconds.toFixed(3)} s`;
}

// Only what the API summary carries. Stage validity sits next to
// the numbers it qualifies (frontend/CLAUDE.md).
export function StageSummaryTable({ summary }: { summary: NonNullable<RunDetail["summary"]> }) {
  return (
    <>
      <Text size="sm">
        Max sustainable RPS: {summary.max_sustainable_rps == null ? "n/a" : `${summary.max_sustainable_rps} req/s`}
      </Text>
      <Table.ScrollContainer minWidth={640}>
        <Table>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Stage</Table.Th>
              <Table.Th>Offered (req/s)</Table.Th>
              <Table.Th>Achieved (req/s)</Table.Th>
              <Table.Th>Requests</Table.Th>
              <Table.Th>Errors</Table.Th>
              <Table.Th>p95 TTFT (client)</Table.Th>
              <Table.Th>p95 E2E (client)</Table.Th>
              <Table.Th>Validity</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {summary.stages.map((stage) => (
              <Table.Tr key={stage.stage_index}>
                <Table.Td>{stage.stage_index}</Table.Td>
                <Table.Td>{stage.offered_rps}</Table.Td>
                <Table.Td>{stage.achieved_rps}</Table.Td>
                <Table.Td>{stage.requests}</Table.Td>
                <Table.Td>{stage.errors}</Table.Td>
                <Table.Td>{formatSeconds(stage.ttft_p95_seconds)}</Table.Td>
                <Table.Td>{formatSeconds(stage.e2e_p95_seconds)}</Table.Td>
                <Table.Td>
                  {!stage.is_valid ? "invalid" : "valid"}
                  {stage.is_client_bound ? ", client-bound" : ""}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
    </>
  );
}
