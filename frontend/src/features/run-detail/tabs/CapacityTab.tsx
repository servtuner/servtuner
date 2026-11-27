import { Alert, Anchor, Group, NumberInput, Stack, Table, Text } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import { useRunDocument } from "../../../api/hooks";
import type { CapacityTable, RunStatus } from "../../../api/types";
import { formatBytes, formatCores, formatNumber, formatUsd } from "../../../components/format";
import { WithDocument } from "./NotAnalysed";

export function CapacityTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const capacity = useRunDocument("getCapacity", runId, status);
  return (
    <Stack gap="md">
      <WithDocument query={capacity} what="capacity table">
        {(table) => <Capacity table={table} />}
      </WithDocument>
      {capacity.data && (
        <Anchor component={Link} to={`/planner?runs=${encodeURIComponent(runId)}`} size="sm">
          Plan capacity with this config
        </Anchor>
      )}
    </Stack>
  );
}

function Capacity({ table }: { table: CapacityTable }) {
  const [traffic, setTraffic] = useState<number | null>(null);
  if (table.rows.length === 0) {
    return (
      <Alert variant="light" color="gray" title="No capacity table">
        {table.reason ?? "No stage was valid and met the SLOs, so there is no per-replica capacity to scale."}
      </Alert>
    );
  }
  const highlighted = traffic == null ? -1 : table.rows.findIndex((row) => row.rps >= traffic);
  const hasGpus = table.rows.some((row) => row.gpus != null);
  const hasCost = table.rows.some((row) => row.cost_per_hour != null);
  return (
    <Stack gap="md">
      <Group justify="space-between" align="end">
        <Text size="sm" maw={620}>
          {formatNumber(table.rps_per_replica)} RPS per replica at the SLOs, with{" "}
          {formatNumber((table.replica_headroom ?? 0) * 100)}% replica headroom.
          {table.is_scale_out_verified ? " Scale-out was measured." : " Scale-out is assumed linear."}
        </Text>
        <NumberInput
          label="Your traffic"
          description="Highlights the row that covers it"
          suffix=" RPS"
          min={0}
          w={200}
          value={traffic ?? ""}
          onChange={(value) => setTraffic(typeof value === "number" ? value : null)}
        />
      </Group>
      <Table.ScrollContainer minWidth={560}>
        <Table verticalSpacing={6} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Traffic</Table.Th>
              <Table.Th>Replicas</Table.Th>
              <Table.Th>CPU requests</Table.Th>
              <Table.Th>Memory requests</Table.Th>
              {hasGpus && <Table.Th>GPUs</Table.Th>}
              {hasCost && <Table.Th>Cost per hour</Table.Th>}
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {table.rows.map((row, index) => (
              <Table.Tr
                key={row.rps}
                bg={index === highlighted ? "var(--mantine-color-teal-light)" : undefined}
                aria-current={index === highlighted ? "true" : undefined}
              >
                <Table.Td>
                  {formatNumber(row.rps)} RPS
                  {row.is_target_rps && (
                    <Text span size="xs" c="dimmed">
                      {" "}
                      (target)
                    </Text>
                  )}
                </Table.Td>
                <Table.Td>{row.replicas}</Table.Td>
                <Table.Td>{formatCores(row.cpu_request_cores)}</Table.Td>
                <Table.Td>{formatBytes(row.memory_request_bytes)}</Table.Td>
                {hasGpus && <Table.Td>{row.gpus ?? "–"}</Table.Td>}
                {hasCost && <Table.Td>{formatUsd(row.cost_per_hour)}</Table.Td>}
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      {traffic != null && highlighted < 0 && (
        <Text size="sm" c="orange.8">
          {formatNumber(traffic)} RPS is beyond the table; the planner extrapolates only with a warning.
        </Text>
      )}
      {table.infra_cost && <InfraCostNote cost={table.infra_cost} />}
      {table.token_cost && <TokenCostNote cost={table.token_cost} />}
      {table.caveats.map((caveat, index) => (
        <Text key={index} size="xs" c="dimmed">
          {String(caveat)}
        </Text>
      ))}
    </Stack>
  );
}

/** What the requests cost in tokens at the SLO-max stage, or why they are unpriced. */
function TokenCostNote({ cost }: { cost: NonNullable<CapacityTable["token_cost"]> }) {
  if (!cost.is_priced) {
    return (
      <Text size="sm" c="dimmed">
        No token cost for {cost.model}: {cost.reason}
      </Text>
    );
  }
  const source = cost.method === "router_metric" ? "the router's own cost" : `the datasheet (${cost.datasheet_key})`;
  return (
    <Stack gap={2}>
      <Text size="sm">
        Tokens cost {formatUsd(cost.usd_per_1k_requests)} per 1k requests and {formatUsd(cost.usd_per_1m_output_tokens)}{" "}
        per 1M output tokens for {cost.model} at stage {cost.stage_index}, from {source}.
        {cost.retry_added_usd_per_1k_requests != null &&
          ` Retries and fallbacks added ${formatUsd(cost.retry_added_usd_per_1k_requests)} per 1k requests.`}
      </Text>
      {(cost.approximations ?? []).map((note) => (
        <Text key={note} size="xs" c="dimmed">
          {note}
        </Text>
      ))}
    </Stack>
  );
}

/** How one replica was priced, or why it was not: never shown as free. */
function InfraCostNote({ cost }: { cost: NonNullable<CapacityTable["infra_cost"]> }) {
  if (!cost.is_priced) {
    return (
      <Text size="sm" c="dimmed">
        No infrastructure cost ({cost.source} prices): {cost.reason}
      </Text>
    );
  }
  return (
    <Text size="sm">
      {formatUsd(cost.usd_per_replica_hour)} per replica-hour from {cost.source} prices:{" "}
      <Text span size="xs" ff="monospace">
        {cost.basis}
      </Text>
    </Text>
  );
}
