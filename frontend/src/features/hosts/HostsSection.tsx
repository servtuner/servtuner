import { Anchor, Button, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import { useHosts, useSaveHost } from "../../api/hooks";
import { QueryError } from "../../components/QueryError";
import { HostForm } from "./HostForm";
import { hardwareLine, priceLine } from "./summary";

/** The machines managed vLLMs run on: each recorded once, with its hardware and price. */
export function HostsSection() {
  const hosts = useHosts();
  const create = useSaveHost();
  const [isAdding, setIsAdding] = useState(false);
  return (
    <Stack gap="sm">
      <Title order={3}>Hosts</Title>
      <Text c="dimmed" size="sm" maw={720}>
        The machines a host vLLM runs on, each with its hardware and price per hour, set once for every target on it.
      </Text>
      {hosts.isPending && <Loader size="sm" aria-label="Loading hosts" />}
      {hosts.isError && <QueryError title="Could not load hosts" error={hosts.error} />}
      {hosts.isSuccess && hosts.data.length > 0 && (
        <Table verticalSpacing={6}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Hardware</Table.Th>
              <Table.Th>Price</Table.Th>
              <Table.Th>Targets</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {hosts.data.map((host) => (
              <Table.Tr key={host.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/hosts/${host.id}`}>
                    {host.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{hardwareLine(host.hardware)}</Table.Td>
                <Table.Td>{priceLine({ usd_per_hour: host.usd_per_hour, price_basis: null })}</Table.Td>
                <Table.Td>{host.target_ids?.length ?? 0}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      {isAdding ? (
        <HostForm
          submitLabel="Save host"
          isPending={create.isPending}
          error={create.error?.message}
          onSubmit={(body) => create.mutate(body, { onSuccess: () => setIsAdding(false) })}
          onCancel={() => setIsAdding(false)}
        />
      ) : (
        <Group>
          <Button variant="default" onClick={() => setIsAdding(true)}>
            Add a host
          </Button>
        </Group>
      )}
    </Stack>
  );
}
