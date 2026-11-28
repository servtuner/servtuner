import { Anchor, Badge, Button, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useManagedTargets, useSaveManagedTarget } from "../../api/hooks";
import type { ManagedTarget } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { HostsSection } from "../hosts/HostsSection";
import { PriceBookSection } from "../hosts/PriceBookSection";
import { TimeValue } from "../../components/TimeValue";
import type { Placement } from "./form";
import { ManagedTargetForm } from "./ManagedTargetForm";
import { deploymentState, placementLine } from "./summary";

/** The vLLMs servtuner deploys itself: on Kubernetes, or on a host through servtuner launcher. */
export function ManagedTargetsPage() {
  const targets = useManagedTargets();
  return (
    <Stack gap="lg">
      <Title order={2}>Managed Servers</Title>
      <Text c="dimmed" maw={720} size="sm">
        servtuner deploys these itself: open one to deploy it with flags, watch it boot, read its log, benchmark it or
        free its GPU. A tuning session deploys one candidate after another on the same target. To benchmark a system
        someone else deploys (a router, an API, a vLLM servtuner did not start), save it under{" "}
        <Anchor component={Link} to="/targets">
          Targets
        </Anchor>{" "}
        instead.
      </Text>
      {targets.isPending && <Loader size="sm" aria-label="Loading managed targets" />}
      {targets.isError && <QueryError title="Could not load managed targets" error={targets.error} />}
      {targets.isSuccess && targets.data.length === 0 && (
        <Text size="sm" c="dimmed">
          No managed servers yet.
        </Text>
      )}
      {targets.isSuccess && targets.data.length > 0 && <ManagedTargetsTable targets={targets.data} />}
      <AddManagedTarget />
      <HostsSection />
      <PriceBookSection />
    </Stack>
  );
}

function ManagedTargetsTable({ targets }: { targets: ManagedTarget[] }) {
  return (
    <Table verticalSpacing={6}>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>Name</Table.Th>
          <Table.Th>Where</Table.Th>
          <Table.Th>Model</Table.Th>
          <Table.Th>GPUs</Table.Th>
          <Table.Th>Transport</Table.Th>
          <Table.Th>State</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {targets.map((target) => (
          <Table.Tr key={target.id}>
            <Table.Td>
              <Anchor component={Link} to={`/managed-targets/${target.id}`}>
                {target.name}
              </Anchor>
            </Table.Td>
            <Table.Td>{placementLine(target)}</Table.Td>
            <Table.Td ff="monospace">{target.spec.model}</Table.Td>
            <Table.Td>{target.spec.placement === "host" ? "–" : target.spec.gpus}</Table.Td>
            <Table.Td>
              {target.spec.placement !== "host" && target.spec.transport?.enabled ? "TLS" : "Plain HTTP"}
            </Table.Td>
            <Table.Td>
              <DeploymentStateCell target={target} />
            </Table.Td>
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

function DeploymentStateCell({ target }: { target: ManagedTarget }) {
  const state = deploymentState(target);
  return (
    <Group gap="xs">
      <Badge variant="light" color={state.color}>
        {state.label}
      </Badge>
      {state.since && <TimeValue timestamp={state.since} />}
    </Group>
  );
}

/**
 * Adds a managed server; the form is shared with the target's own page (edit). ?add=host or
 * ?add=kubernetes opens it in that placement, and ?next=tuning goes on to New session with the
 * new target picked (Tuning's "Add a vLLM host").
 */
function AddManagedTarget() {
  const create = useSaveManagedTarget();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const asked = searchParams.get("add");
  const placement: Placement | undefined = asked === "host" || asked === "kubernetes" ? asked : undefined;
  const isNextTuning = searchParams.get("next") === "tuning";
  const [isOpen, setIsOpen] = useState(placement !== undefined);
  if (!isOpen)
    return (
      <Group>
        <Button variant="default" onClick={() => setIsOpen(true)}>
          Add a managed server
        </Button>
      </Group>
    );
  return (
    <ManagedTargetForm
      initialPlacement={placement}
      submitLabel={isNextTuning ? "Save and tune it" : "Save managed target"}
      isPending={create.isPending}
      error={create.error?.message}
      onSubmit={(save) =>
        create.mutate(save, {
          onSuccess: (saved) => (isNextTuning ? navigate(`/tuning/new?target=${saved.id}`) : setIsOpen(false)),
        })
      }
      onCancel={() => (isNextTuning ? navigate("/tuning") : setIsOpen(false))}
    />
  );
}
