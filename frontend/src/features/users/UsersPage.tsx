import {
  Alert,
  Badge,
  Button,
  Code,
  Group,
  Modal,
  PasswordInput,
  Select,
  Stack,
  Table,
  Text,
  Textarea,
  TextInput,
  Title,
} from "@mantine/core";
import { type FormEvent, useState } from "react";
import { useAudit, useAuthStatus, useUsers } from "../../api/auth";
import type { Role, UserAdmin } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { authErrorText } from "../auth/errors";

const ROLES: { value: Role; label: string }[] = [
  { value: "viewer", label: "Viewer: reads everything" },
  { value: "operator", label: "Operator: runs, schedules, tuning" },
  { value: "admin", label: "Admin: targets, secrets, deletes, users" },
];

/** One subject per line: DNs hold commas, so commas do not separate. */
function subjectsOf(text: string): string[] {
  return text
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);
}

function NewUserForm() {
  const { create } = useUsers();
  const [username, setUsername] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [password, setPassword] = useState("");
  const [subjects, setSubjects] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    create.mutate(
      { username, role, password: password || null, subjects: subjectsOf(subjects) },
      {
        onSuccess: () => {
          setUsername("");
          setPassword("");
          setSubjects("");
        },
      },
    );
  };
  return (
    <form onSubmit={submit}>
      <Stack gap="sm" maw={520}>
        <Title order={3}>Add a user</Title>
        <Group grow align="flex-start">
          <TextInput
            label="Username"
            required
            value={username}
            onChange={(event) => setUsername(event.currentTarget.value)}
          />
          <Select
            label="Role"
            data={ROLES}
            value={role}
            allowDeselect={false}
            onChange={(value) => value && setRole(value as Role)}
          />
        </Group>
        <PasswordInput
          label="Temporary password"
          description="They choose their own at first sign-in. Leave empty for a proxy-only user."
          autoComplete="new-password"
          value={password}
          onChange={(event) => setPassword(event.currentTarget.value)}
        />
        <Textarea
          label="Proxy identities"
          description="What the proxy sends as the user (username, email or DN), one per line."
          autosize
          minRows={2}
          spellCheck={false}
          value={subjects}
          onChange={(event) => setSubjects(event.currentTarget.value)}
        />
        {create.error && (
          <Alert color="red" role="alert">
            {authErrorText(create.error)}
          </Alert>
        )}
        <Group>
          <Button type="submit" loading={create.isPending}>
            Add user
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

function IdentityCell({ user }: { user: UserAdmin }) {
  const { link, unlink } = useUsers();
  const [subject, setSubject] = useState("");
  return (
    <Stack gap={4}>
      {user.identities.map((identity) => (
        <Group key={identity.subject} gap={4} wrap="nowrap">
          <Code>{identity.subject}</Code>
          <Button
            size="compact-xs"
            variant="subtle"
            color="red"
            aria-label={`Unlink ${identity.subject}`}
            onClick={() => unlink.mutate({ id: user.id, subject: identity.subject })}
          >
            ✕
          </Button>
        </Group>
      ))}
      <Group gap="xs" wrap="nowrap" align="center">
        {user.has_password && (
          <Badge variant="outline" style={{ flexShrink: 0 }}>
            Password
          </Badge>
        )}
        <TextInput
          size="xs"
          w={220}
          placeholder="Link a proxy identity"
          aria-label={`Proxy identity for ${user.username}`}
          value={subject}
          onChange={(event) => setSubject(event.currentTarget.value)}
        />
        <Button
          size="xs"
          variant="light"
          disabled={!subject.trim()}
          onClick={() => link.mutate({ id: user.id, subject: subject.trim() }, { onSuccess: () => setSubject("") })}
        >
          Link
        </Button>
      </Group>
    </Stack>
  );
}

function UsersTable() {
  const { users, update, resetPassword, revokeSessions } = useUsers();
  const { data: status } = useAuthStatus();
  const [temporary, setTemporary] = useState<{ username: string; password: string } | null>(null);
  const failure = update.error ?? resetPassword.error ?? revokeSessions.error;
  if (users.isError) return <QueryError title="Users could not be loaded" error={users.error} />;
  return (
    <Stack gap="sm">
      {failure && (
        <Alert color="red" role="alert">
          {authErrorText(failure)}
        </Alert>
      )}
      <Table.ScrollContainer minWidth={960}>
        <Table striped verticalSpacing="sm" styles={{ td: { verticalAlign: "middle" } }}>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>User</Table.Th>
              <Table.Th>Role</Table.Th>
              <Table.Th>Signs in with</Table.Th>
              <Table.Th>Added</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {(users.data ?? []).map((user) => (
              <Table.Tr key={user.id} opacity={user.is_disabled ? 0.6 : 1}>
                <Table.Td>
                  <Text size="sm" fw={500}>
                    {user.username}
                    {user.id === status?.user?.id && " (you)"}
                  </Text>
                  {user.display_name && (
                    <Text size="xs" c="dimmed">
                      {user.display_name}
                    </Text>
                  )}
                  {user.is_disabled && <Badge color="gray">Disabled</Badge>}
                  {user.must_change_password && <Badge variant="light">Must change password</Badge>}
                </Table.Td>
                <Table.Td>
                  <Select
                    size="xs"
                    w={130}
                    aria-label={`Role of ${user.username}`}
                    data={ROLES.map(({ value }) => value)}
                    value={user.role}
                    allowDeselect={false}
                    onChange={(value) => value && update.mutate({ id: user.id, body: { role: value as Role } })}
                  />
                </Table.Td>
                <Table.Td>
                  <IdentityCell user={user} />
                </Table.Td>
                <Table.Td>
                  <TimeValue timestamp={user.created_at} />
                </Table.Td>
                <Table.Td>
                  <Group gap={4} wrap="nowrap">
                    <Button
                      size="xs"
                      variant="subtle"
                      onClick={() =>
                        resetPassword.mutate(user.id, {
                          onSuccess: ({ password }) => setTemporary({ username: user.username, password }),
                        })
                      }
                    >
                      Reset password
                    </Button>
                    <Button size="xs" variant="subtle" onClick={() => revokeSessions.mutate(user.id)}>
                      Sign out
                    </Button>
                    <Button
                      size="xs"
                      variant="subtle"
                      color={user.is_disabled ? "blue" : "red"}
                      onClick={() => update.mutate({ id: user.id, body: { is_disabled: !user.is_disabled } })}
                    >
                      {user.is_disabled ? "Enable" : "Disable"}
                    </Button>
                  </Group>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      </Table.ScrollContainer>
      <Modal opened={temporary !== null} onClose={() => setTemporary(null)} title="Temporary password" centered>
        <Stack gap="sm">
          <Text size="sm">
            Give this to {temporary?.username}. It is shown once; they must change it when they sign in.
          </Text>
          <Code block>{temporary?.password}</Code>
        </Stack>
      </Modal>
    </Stack>
  );
}

function AuditLog() {
  const audit = useAudit();
  if (audit.isError) return <QueryError title="The audit log could not be loaded" error={audit.error} />;
  return (
    <Stack gap="sm">
      <Title order={3}>Audit log</Title>
      <Table>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>When</Table.Th>
            <Table.Th>What</Table.Th>
            <Table.Th>Outcome</Table.Th>
            <Table.Th>Identity</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {(audit.data ?? []).map((event) => (
            <Table.Tr key={event.id}>
              <Table.Td>
                <TimeValue timestamp={event.at} />
              </Table.Td>
              <Table.Td>{event.action.replaceAll("_", " ")}</Table.Td>
              <Table.Td>
                <Badge color={event.outcome === "success" ? "green" : "red"} variant="light">
                  {event.outcome}
                </Badge>
              </Table.Td>
              <Table.Td>{event.subject ? <Code>{event.subject}</Code> : "-"}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Stack>
  );
}

/** Users, roles, proxy identities, resets and the audit log (admins only). */
export function UsersPage() {
  return (
    <Stack gap="xl">
      <Title order={2}>Users</Title>
      <UsersTable />
      <NewUserForm />
      <AuditLog />
    </Stack>
  );
}
