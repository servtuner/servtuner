import {
  ActionIcon,
  AppShell,
  Group,
  Menu,
  Text,
  UnstyledButton,
  useComputedColorScheme,
  useMantineColorScheme,
} from "@mantine/core";
import { Link, Outlet } from "react-router";
import { AuthGate } from "../features/auth/AuthGate";
import { UserMenu } from "../features/auth/UserMenu";

function ColorSchemeToggle() {
  const { setColorScheme } = useMantineColorScheme();
  // Read on the first render, so the toggle never shows the wrong icon first.
  const computedColorScheme = useComputedColorScheme("light", { getInitialValueInEffect: false });
  const nextColorScheme = computedColorScheme === "light" ? "dark" : "light";
  return (
    <ActionIcon
      variant="default"
      aria-label={`Switch to ${nextColorScheme} theme`}
      onClick={() => setColorScheme(nextColorScheme)}
    >
      {computedColorScheme === "light" ? "☾" : "☀"}
    </ActionIcon>
  );
}

type NavItem = [to: string, label: string];

// The main job up top: deploy and tune a vLLM, then read the runs. The rest is one menu away.
const NAV: NavItem[] = [
  ["/managed-targets", "Managed vLLM"],
  ["/tuning", "Tuning"],
  ["/runs", "Runs"],
  ["/experiments", "Experiments"],
];
const MORE: NavItem[] = [
  ["/planner", "Planner"],
  ["/schedules", "Schedules"],
  ["/targets", "Targets"],
];

export function Layout() {
  return (
    <AuthGate>
      <AppShell header={{ height: 56 }} padding="md">
        <AppShell.Header>
          <Group h="100%" px="md" justify="space-between" wrap="nowrap">
            <Group gap="lg" wrap="nowrap" style={{ minWidth: 0, overflow: "hidden" }}>
              <Text component={Link} to="/" fw={700} size="lg" td="none" c="inherit">
                servtuner
              </Text>
              {NAV.map(([to, label]) => (
                <Text key={to} component={Link} to={to} size="sm" c="dimmed" td="none">
                  {label}
                </Text>
              ))}
              <Menu position="bottom-start" withinPortal>
                <Menu.Target>
                  <UnstyledButton aria-label="More pages">
                    <Text size="sm" c="dimmed">
                      More ▾
                    </Text>
                  </UnstyledButton>
                </Menu.Target>
                <Menu.Dropdown>
                  {MORE.map(([to, label]) => (
                    <Menu.Item key={to} component={Link} to={to}>
                      {label}
                    </Menu.Item>
                  ))}
                </Menu.Dropdown>
              </Menu>
            </Group>
            <Group gap="md" wrap="nowrap" style={{ flexShrink: 0 }}>
              <UserMenu />
              <ColorSchemeToggle />
            </Group>
          </Group>
        </AppShell.Header>
        <AppShell.Main>
          <Outlet />
        </AppShell.Main>
      </AppShell>
    </AuthGate>
  );
}
