import { Badge, Button, Group, Stack, Table, Text, Title } from "@mantine/core";
import { useAuthStatus, useMySessions } from "../../api/auth";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { ChangePasswordForm } from "./ChangePasswordForm";

function Sessions() {
  const { sessions, revoke, revokeOthers } = useMySessions();
  if (sessions.isError) return <QueryError title="Your sessions could not be loaded" error={sessions.error} />;
  const rows = sessions.data ?? [];
  return (
    <Stack gap="sm">
      <Group justify="space-between">
        <Title order={3}>Sessions</Title>
        <Button variant="light" loading={revokeOthers.isPending} onClick={() => revokeOthers.mutate()}>
          Sign out everywhere else
        </Button>
      </Group>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Signed in</Table.Th>
            <Table.Th>Last used</Table.Th>
            <Table.Th>Ends</Table.Th>
            <Table.Th>Browser</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {rows.map((session) => (
            <Table.Tr key={session.id}>
              <Table.Td>
                <TimeValue timestamp={session.created_at} />
              </Table.Td>
              <Table.Td>
                <TimeValue timestamp={session.last_seen_at} />
              </Table.Td>
              <Table.Td>
                <TimeValue timestamp={session.expires_at} />
              </Table.Td>
              <Table.Td>
                <Text size="xs" lineClamp={1}>
                  {session.user_agent ?? "-"}
                </Text>
              </Table.Td>
              <Table.Td>
                {session.is_current ? (
                  <Badge variant="light">This one</Badge>
                ) : (
                  <Button size="xs" variant="subtle" onClick={() => revoke.mutate(session.id)}>
                    Sign out
                  </Button>
                )}
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

export function AccountPage() {
  const { data: status } = useAuthStatus();
  const user = status?.user;
  if (!user) return <Text>User auth is off on this install (auth.mode none).</Text>;
  const isLocal = user.auth_method === "session";
  return (
    <Stack gap="xl">
      <Stack gap={4}>
        <Title order={2}>{user.display_name ?? user.username}</Title>
        <Text size="sm" c="dimmed">
          {user.username} · {user.role} · {isLocal ? "signed in with a password" : "signed in through the proxy"}
        </Text>
      </Stack>
      {isLocal ? (
        <>
          <Stack gap="sm">
            <Title order={3}>Password</Title>
            <ChangePasswordForm />
          </Stack>
          <Sessions />
        </>
      ) : (
        <Text size="sm">Your proxy signs you in and out; servtuner keeps no session for you.</Text>
      )}
    </Stack>
  );
}
