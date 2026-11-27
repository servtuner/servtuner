import { Badge, Group, Menu, Text, UnstyledButton } from "@mantine/core";
import { Link } from "react-router";
import { useAuthStatus, useCan, useLogout } from "../../api/auth";

/** Who is signed in, and how; nothing under auth.mode none. */
export function UserMenu() {
  const { data: status } = useAuthStatus();
  const logout = useLogout();
  const canAdminUsers = useCan("users.admin");
  const user = status?.user;
  if (!user) return null;
  return (
    <Menu position="bottom-end" withinPortal>
      <Menu.Target>
        <UnstyledButton aria-label="Account menu" style={{ flexShrink: 0 }}>
          <Group gap={6} wrap="nowrap">
            <Text size="sm" style={{ whiteSpace: "nowrap" }}>
              {user.display_name ?? user.username}
            </Text>
            <Badge size="sm" variant="light" color="gray">
              {user.role}
            </Badge>
          </Group>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Item component={Link} to="/account">
          Account
        </Menu.Item>
        {canAdminUsers && (
          <Menu.Item component={Link} to="/users">
            Users
          </Menu.Item>
        )}
        {/* A proxy user's proxy owns sign-out; servtuner has no session of theirs to end. */}
        {user.auth_method === "session" && <Menu.Item onClick={() => logout.mutate()}>Sign out</Menu.Item>}
      </Menu.Dropdown>
    </Menu>
  );
}
