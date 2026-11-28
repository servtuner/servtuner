import { Anchor, Badge, Button, Group, Loader, Paper, Stack, Table, Text, Title } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useCreateTarget, useTargets } from "../../api/hooks";
import type { SavedTarget } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { TargetForm } from "./TargetForm";
import { Allowed } from "../auth/Allowed";

const CHECK_BADGE = {
  ok: { color: "teal", label: "✓ reachable" },
  warning: { color: "yellow", label: "! warnings" },
  failed: { color: "red", label: "✕ failing" },
} as const;

function KeyBadge({ target }: { target: SavedTarget }) {
  const key = target.credentials.api_key;
  return key?.is_set ? (
    <Badge color="teal" variant="light">
      API key set
    </Badge>
  ) : (
    <Badge color="gray" variant="light">
      No API key
    </Badge>
  );
}

/** The systems a user benchmarks, saved once with their keys. */
export function TargetsPage() {
  const targets = useTargets();
  const create = useCreateTarget();
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);

  return (
    <Stack gap="md">
      <Group justify="space-between">
        <Title order={2}>Targets</Title>
        {!creating && (
          <Allowed action="targets.write">
            <Button onClick={() => setCreating(true)}>New target</Button>
          </Allowed>
        )}
      </Group>
      <Text size="sm" c="dimmed" maw={720}>
        Save each system you benchmark once, with its API key, then pick it on New run. Keys are write-only: servtuner
        stores them in Kubernetes Secrets and only ever shows a masked preview. Anyone who can open this page can start
        runs with a saved key or replace it, but no one can read it back.
      </Text>
      <Text size="sm" maw={720}>
        These are systems someone else deploys (a router, an API, a vLLM you run yourself). To have servtuner deploy a
        vLLM and change its flags for you, add it under{" "}
        <Anchor component={Link} to="/managed-targets">
          Managed Servers
        </Anchor>
        .
      </Text>
      {creating && (
        <Paper withBorder p="md" radius="sm">
          <Title order={4} mb="sm">
            New target
          </Title>
          <TargetForm
            submitLabel="Save target"
            isPending={create.isPending}
            error={create.error?.message}
            onCancel={() => setCreating(false)}
            onSubmit={(body) => create.mutate(body, { onSuccess: (saved) => navigate(`/targets/${saved.id}`) })}
          />
        </Paper>
      )}
      {targets.isPending ? (
        <Loader size="sm" aria-label="Loading targets" />
      ) : targets.isError ? (
        <QueryError title="Could not load targets" error={targets.error} />
      ) : targets.data.length === 0 ? (
        <Text c="dimmed">No saved targets yet.</Text>
      ) : (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Type</Table.Th>
              <Table.Th>Endpoint</Table.Th>
              <Table.Th>Key</Table.Th>
              <Table.Th>Last check</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {targets.data.map((target) => {
              const check = target.last_check ? CHECK_BADGE[target.last_check.result.status] : undefined;
              return (
                <Table.Tr key={target.id}>
                  <Table.Td>
                    <Anchor component={Link} to={`/targets/${target.id}`}>
                      {target.name}
                    </Anchor>
                  </Table.Td>
                  <Table.Td>{target.type}</Table.Td>
                  <Table.Td>
                    <Text size="sm" ff="monospace">
                      {target.target.endpoint}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <KeyBadge target={target} />
                  </Table.Td>
                  <Table.Td>
                    {check ? (
                      <Badge color={check.color} variant="light">
                        {check.label}
                      </Badge>
                    ) : (
                      <Text size="sm" c="dimmed">
                        not checked
                      </Text>
                    )}
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
