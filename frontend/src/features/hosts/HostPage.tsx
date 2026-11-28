import { Alert, Anchor, Button, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useDeleteHost, useHost, useManagedTargets, useReadHostMachine, useSaveHost } from "../../api/hooks";
import type { Host } from "../../api/types";
import { ConfirmButton } from "../../components/ConfirmButton";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { HostForm } from "./HostForm";
import { hardwareLine, priceLine } from "./summary";

/** One host: what the machine is, what it costs, and the managed servers on it. */
export function HostPage() {
  const { hostId = "" } = useParams();
  const host = useHost(hostId);
  if (host.isPending) return <Loader aria-label="Loading host" />;
  if (host.isError)
    return (
      <Stack>
        <QueryError title="Could not load the host" error={host.error} />
        <Anchor component={Link} to="/managed-targets">
          Back to Managed Servers
        </Anchor>
      </Stack>
    );
  return <HostView host={host.data} />;
}

function HostView({ host }: { host: Host }) {
  const [isEditing, setIsEditing] = useState(false);
  const save = useSaveHost(host.id);
  const read = useReadHostMachine(host.id);
  const remove = useDeleteHost();
  const navigate = useNavigate();
  const targets = useManagedTargets();
  // Whether targets use it comes from the host itself; their names from the targets list.
  const isUsed = (host.target_ids?.length ?? 0) > 0;
  const onHost = (targets.data ?? []).filter((target) => host.target_ids?.includes(target.id));
  const hardware = host.hardware;
  return (
    <Stack gap="lg">
      <Anchor component={Link} to="/managed-targets" size="sm">
        Managed Servers
      </Anchor>
      <Group justify="space-between" align="end">
        <Stack gap={0}>
          <Title order={2}>{host.name}</Title>
          {host.note && (
            <Text size="sm" c="dimmed">
              {host.note}
            </Text>
          )}
        </Stack>
        {!isEditing && (
          <Group>
            <Button
              variant="default"
              loading={read.isPending}
              disabled={!isUsed}
              title={isUsed ? undefined : "No managed server runs on it yet"}
              onClick={() => read.mutate()}
            >
              Read from the launcher
            </Button>
            <Button variant="default" onClick={() => setIsEditing(true)}>
              Edit
            </Button>
          </Group>
        )}
      </Group>
      {read.isError && (
        <Alert color="red" title="Could not read the machine">
          {read.error.message}
        </Alert>
      )}
      {isEditing ? (
        <HostForm
          key={host.updated_at}
          existing={host}
          submitLabel="Save host"
          isPending={save.isPending}
          error={save.error?.message}
          onSubmit={(body) => save.mutate(body, { onSuccess: () => setIsEditing(false) })}
          onCancel={() => setIsEditing(false)}
        />
      ) : (
        <Table variant="vertical" withTableBorder maw={900}>
          <Table.Tbody>
            <Table.Tr>
              <Table.Th w={200}>Hardware</Table.Th>
              <Table.Td>
                {hardwareLine(hardware)}
                {hardware?.cpu_model && (
                  <Text size="xs" c="dimmed">
                    {hardware.cpu_model}
                  </Text>
                )}
                <Text size="xs" c="dimmed">
                  {hardware?.source === "launcher" && hardware.read_at ? (
                    <>
                      Read from the launcher <TimeValue timestamp={hardware.read_at} />
                    </>
                  ) : (
                    "Typed in"
                  )}
                </Text>
              </Table.Td>
            </Table.Tr>
            <Table.Tr>
              <Table.Th>Price</Table.Th>
              <Table.Td>{priceLine(host)}</Table.Td>
            </Table.Tr>
            <Table.Tr>
              <Table.Th>Managed servers on it</Table.Th>
              <Table.Td>
                {!isUsed
                  ? "None yet: pick this host on a host vLLM's form"
                  : onHost.map((target, index) => (
                      <span key={target.id}>
                        {index > 0 && ", "}
                        <Anchor component={Link} to={`/managed-targets/${target.id}`}>
                          {target.name}
                        </Anchor>
                      </span>
                    ))}
              </Table.Td>
            </Table.Tr>
          </Table.Tbody>
        </Table>
      )}
      {remove.isError && (
        <Alert color="red" title="Not deleted">
          {remove.error.message}
        </Alert>
      )}
      <Group>
        <ConfirmButton
          label="Delete host"
          title={`Delete ${host.name}?`}
          message={
            isUsed
              ? "Managed servers still run on it: point them at another host first."
              : "Its hardware and price go. Past runs keep what they recorded."
          }
          confirmLabel="Delete"
          isPending={remove.isPending}
          isConfirmDisabled={isUsed}
          onConfirm={() => remove.mutate(host.id, { onSuccess: () => navigate("/managed-targets") })}
        />
      </Group>
    </Stack>
  );
}
