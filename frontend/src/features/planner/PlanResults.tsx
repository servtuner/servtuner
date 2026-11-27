import { Badge, Group, List, Stack, Table, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";
import type { Plan } from "../../api/types";
import { formatBytes, formatCores, formatNumber, formatPercent } from "../../components/format";

type Tier = Plan["tiers"][number];
type Check = Plan["checks"][number];

const CHECK_COLOR: Record<Check["status"], string> = {
  ok: "teal",
  warning: "yellow",
  violates_measured: "orange",
  blocked: "red",
};
const CHECK_LABEL: Record<Check["status"], string> = {
  ok: "OK",
  warning: "Warning",
  violates_measured: "Violates measured",
  blocked: "Blocked",
};
const CONFIDENCE_COLOR: Record<Tier["confidence"], string> = { high: "teal", medium: "yellow", low: "gray" };

/** The plan per tier, each value against the baseline when overrides change it. */
export function PlanResults({ plan, baseline, stale }: { plan: Plan; baseline?: Plan; stale: boolean }) {
  return (
    <Stack gap="lg" style={{ opacity: stale ? 0.6 : 1 }} aria-busy={stale}>
      <Checks checks={plan.checks} />
      {plan.tiers.map((tier) => (
        <TierCard key={tier.key} tier={tier} before={baseline?.tiers.find((other) => other.key === tier.key)} />
      ))}
    </Stack>
  );
}

function Checks({ checks }: { checks: Check[] }) {
  if (checks.length === 0) return null;
  return (
    <Stack gap={4}>
      <Title order={4}>Checks</Title>
      {checks.map((check, index) => (
        <Group key={index} gap="xs" wrap="nowrap" align="start">
          <Badge color={CHECK_COLOR[check.status]} variant="light" size="sm" miw={130}>
            {CHECK_LABEL[check.status]}
          </Badge>
          <Text size="sm">
            {check.tier && <Text span c="dimmed">{`${check.tier}: `}</Text>}
            {check.text}
          </Text>
        </Group>
      ))}
    </Stack>
  );
}

/** "before → after" when an override moved the value, otherwise just the value. */
function Delta({
  value,
  before,
  format,
}: {
  value: number | null | undefined;
  before?: number | null;
  format: (n: number) => string;
}) {
  if (value == null) return <>–</>;
  if (before == null || before === value) return <>{format(value)}</>;
  return (
    <>
      <Text span c="dimmed" td="line-through">
        {format(before)}
      </Text>{" "}
      {format(value)}
    </>
  );
}

function TierCard({ tier, before }: { tier: Tier; before?: Tier }) {
  const count = (n: number) => n.toLocaleString("en-US");
  const rows: [string, ReactNode][] = [
    ["Replicas at peak", <Delta key="peak" value={tier.replicas_peak} before={before?.replicas_peak} format={count} />],
    [
      "Replicas at baseline",
      <Delta key="base" value={tier.replicas_baseline} before={before?.replicas_baseline} format={count} />,
    ],
    [
      "RPS per replica at the SLO",
      <>
        <Delta
          key="rps"
          value={tier.rps_per_replica}
          before={before?.rps_per_replica}
          format={(n) => formatNumber(n)}
        />
        {tier.is_interpolated && (
          <Text span size="xs" c="dimmed">
            {" "}
            interpolated between stages
          </Text>
        )}
      </>,
    ],
    ["Limiting SLO", tier.limiting_slo ?? "–"],
    [
      "HPA",
      tier.hpa
        ? `min ${tier.hpa.min_replicas}, max ${tier.hpa.max_replicas}${
            tier.hpa.cpu_utilization_percent != null
              ? `, target ${formatNumber(tier.hpa.cpu_utilization_percent)}% CPU`
              : ""
          }`
        : "–",
    ],
    [
      "Per replica",
      tier.per_replica
        ? `${formatCores(tier.per_replica.cpu_request_cores)}, ${formatBytes(tier.per_replica.memory_request_bytes)}${
            tier.per_replica.gpus ? `, ${tier.per_replica.gpus} GPU` : ""
          }`
        : "–",
    ],
    [
      "Totals",
      tier.totals ? (
        <>
          <Delta
            key="cpu"
            value={tier.totals.cpu_request_cores}
            before={before?.totals?.cpu_request_cores}
            format={formatCores}
          />
          ,{" "}
          <Delta
            key="mem"
            value={tier.totals.memory_request_bytes}
            before={before?.totals?.memory_request_bytes}
            format={formatBytes}
          />
          {tier.totals.gpus ? `, ${tier.totals.gpus} GPUs` : ""}
        </>
      ) : (
        "–"
      ),
    ],
    [
      "Utilization at peak",
      <Delta
        key="util"
        value={tier.utilization_at_peak}
        before={before?.utilization_at_peak}
        format={(n) => `${formatPercent(n)} of SLO-max`}
      />,
    ],
    [
      "Router queue per replica",
      tier.queue ? (
        <>
          concurrency (C){" "}
          <Delta key="c" value={tier.queue.concurrency} before={before?.queue?.concurrency} format={count} />
          {tier.queue.buffer_size != null && (
            <>
              , queue size (B){" "}
              <Delta key="b" value={tier.queue.buffer_size} before={before?.queue?.buffer_size} format={count} />
            </>
          )}
        </>
      ) : (
        "–"
      ),
    ],
  ];
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Title order={4}>
          {tier.key}{" "}
          <Text span c="dimmed" size="sm">
            {tier.target_type}, from run {tier.run_id}
          </Text>
        </Title>
        <Badge color={CONFIDENCE_COLOR[tier.confidence]} variant="light">
          {tier.confidence} confidence
        </Badge>
      </Group>
      {tier.is_blocked ? (
        <Text c="red">Blocked: {tier.blocked_reason}</Text>
      ) : (
        <Table verticalSpacing={4} style={{ fontVariantNumeric: "tabular-nums" }}>
          <Table.Tbody>
            {rows.map(([label, value]) => (
              <Table.Tr key={label}>
                <Table.Td c="dimmed" w={220}>
                  {label}
                </Table.Td>
                <Table.Td>{value}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {tier.sensitivity && tier.sensitivity.length > 0 && (
        <Text size="sm">
          If traffic grows:{" "}
          {tier.sensitivity
            .map((step) => `×${formatNumber(step.traffic_factor)} (${formatNumber(step.rps)} RPS) → ${step.replicas}`)
            .join("; ")}{" "}
          replicas.
        </Text>
      )}
      {tier.scope && tier.scope.length > 0 && (
        <Group gap={4}>
          {tier.scope.map((label) => (
            <Badge key={label} variant="outline" color="gray" size="sm" tt="none">
              {label}
            </Badge>
          ))}
        </Group>
      )}
      {(tier.warnings ?? []).length > 0 && (
        <List size="sm" c="orange">
          {(tier.warnings ?? []).map((warning) => (
            <List.Item key={warning}>{warning}</List.Item>
          ))}
        </List>
      )}
    </Stack>
  );
}
