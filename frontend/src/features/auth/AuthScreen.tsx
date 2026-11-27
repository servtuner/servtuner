import { Center, Paper, Stack, Text, Title } from "@mantine/core";
import type { ReactNode } from "react";

/** The frame of the screens shown instead of the app: setup, sign-in, a refused identity. */
export function AuthScreen({ title, intro, children }: { title: string; intro?: ReactNode; children?: ReactNode }) {
  return (
    <Center mih="100vh" p="md">
      <Paper withBorder shadow="sm" p="xl" radius="md" w={420} maw="100%">
        <Stack gap="md">
          <Text fw={700} size="lg">
            servtuner
          </Text>
          <Title order={2}>{title}</Title>
          {typeof intro === "string" ? <Text size="sm">{intro}</Text> : intro}
          {children}
        </Stack>
      </Paper>
    </Center>
  );
}
