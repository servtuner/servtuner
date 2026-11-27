import { ActionIcon, Badge, Button, Group, NumberInput, Stack, Table, Text, Title, Tooltip } from "@mantine/core";
import type { Plan, PlanRequest } from "../../api/types";
import { formatNumber } from "../../components/format";
import { QueueTerms } from "../../components/QueueTerms";
import { mentionsQueueTerms } from "../../components/queueTermDefinitions";
import { UnitValue } from "../../components/UnitValue";

type DerivationNode = Plan["derivation"]["nodes"][number];
type Override = NonNullable<PlanRequest["overrides"]>[number];
export type Overrides = Map<string, Override>;

// How each kind of node is changed: parameters within their
// bounds, derived values pinned, hypothetical inputs set. Measured nodes are
// evidence and are not edited here.
const OVERRIDE_KIND: Partial<Record<DerivationNode["kind"], Override["kind"]>> = {
  parameter: "parameter",
  derived: "pin",
  hypothetical: "hypothetical",
};

// Parameters the left panel already sets: an override here would silently
// fight the input, so these are changed there (node ids are "<tier>.<name>").
const SET_BY_INPUT = new Set(["peak_rps", "h_rep", "min_replicas", "spare", "zones"]);

const KIND_COLOR: Record<DerivationNode["kind"], string> = {
  measured: "blue",
  parameter: "grape",
  derived: "gray",
  hypothetical: "orange",
};

/** The scenario workbench's calculation tree. servtuner evaluates every change. */
export function CalculationTree({
  nodes,
  overrides,
  onOverrides,
}: {
  nodes: DerivationNode[];
  overrides: Overrides;
  onOverrides: (overrides: Overrides) => void;
}) {
  const set = (node: DerivationNode, value: number | undefined) => {
    const next = new Map(overrides);
    const kind = OVERRIDE_KIND[node.kind];
    if (value === undefined || kind === undefined) next.delete(node.id);
    else next.set(node.id, { node: node.id, kind, value });
    onOverrides(next);
  };
  return (
    <Stack gap="xs">
      <Group justify="space-between">
        <Title order={4}>How the plan is calculated</Title>
        {overrides.size > 0 && (
          <Button size="xs" variant="subtle" onClick={() => onOverrides(new Map())}>
            Reset all ({overrides.size})
          </Button>
        )}
      </Group>
      <Text size="xs" c="dimmed">
        Change a parameter, or pin a derived value, to see what follows; servtuner recomputes the plan and its checks.
        Values crossed out are the plan without your changes.
      </Text>
      {mentionsQueueTerms(nodes.map((node) => node.label)) && <QueueTerms />}
      <Table verticalSpacing={4} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Step</Table.Th>
            <Table.Th>How</Table.Th>
            <Table.Th ta="right">Value</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {nodes.map((node) => (
            <NodeRow
              key={node.id}
              node={node}
              override={overrides.get(node.id)}
              onChange={(value) => set(node, value)}
            />
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

function NodeRow({
  node,
  override,
  onChange,
}: {
  node: DerivationNode;
  override: Override | undefined;
  onChange: (value: number | undefined) => void;
}) {
  const setByInput = SET_BY_INPUT.has(node.id.slice(node.id.indexOf(".") + 1));
  const editable = OVERRIDE_KIND[node.kind] !== undefined && !setByInput;
  return (
    <Table.Tr>
      <Table.Td>
        <Group gap={6} wrap="nowrap">
          <Badge size="xs" variant="light" color={KIND_COLOR[node.kind]} style={{ flexShrink: 0 }}>
            {node.kind}
          </Badge>
          <Text size="sm">{node.label}</Text>
        </Group>
      </Table.Td>
      <Table.Td>
        <Text size="xs" ff="monospace">
          {node.rendered ?? node.formula ?? ""}
        </Text>
        {node.default != null && node.kind === "parameter" && (
          <Text size="xs" c="dimmed">
            default {formatNumber(node.default)}
            {node.bounds && ` (${formatNumber(node.bounds.min)} to ${formatNumber(node.bounds.max)})`}
          </Text>
        )}
      </Table.Td>
      <Table.Td ta="right">
        {editable && override ? (
          <Group gap={4} justify="end" wrap="nowrap">
            <NumberInput
              aria-label={`${node.label}, ${override.kind}`}
              size="xs"
              w={110}
              value={override.value}
              min={node.bounds?.min ?? undefined}
              max={node.bounds?.max ?? undefined}
              decimalScale={4}
              onChange={(value) => typeof value === "number" && onChange(value)}
            />
            <Tooltip label="Reset to the computed value">
              <ActionIcon
                variant="subtle"
                color="gray"
                aria-label={`Reset ${node.label}`}
                onClick={() => onChange(undefined)}
              >
                ↺
              </ActionIcon>
            </Tooltip>
          </Group>
        ) : (
          <Group gap={6} justify="end" wrap="nowrap">
            <UnitValue value={node.value} unit={node.unit} />
            {setByInput && (
              <Text size="xs" c="dimmed">
                set on the left
              </Text>
            )}
            {editable && (
              <Button size="compact-xs" variant="subtle" onClick={() => onChange(node.value)}>
                {node.kind === "derived" ? "Pin" : "Change"}
              </Button>
            )}
          </Group>
        )}
      </Table.Td>
    </Table.Tr>
  );
}
