import { Badge, Group, Paper, Stack, Text } from "@mantine/core";
import { OTHER_SYMBOLS, QUEUE_TERMS } from "./queueTermDefinitions";

/** A key to C, B and the symbols around them, shown beside anything that uses them. */
export function QueueTerms() {
  return (
    <Paper withBorder p="sm" radius="sm" bg="var(--mantine-color-default-hover)" aria-label="What C and B mean">
      <Stack gap={6}>
        {(Object.keys(QUEUE_TERMS) as (keyof typeof QUEUE_TERMS)[]).map((symbol) => (
          <Group key={symbol} gap="xs" wrap="nowrap" align="baseline">
            <QueueTermBadge symbol={symbol} />
            <Text size="sm">{QUEUE_TERMS[symbol].meaning}.</Text>
          </Group>
        ))}
        <Text size="xs" c="dimmed">
          {OTHER_SYMBOLS.map(([symbol, meaning], index) => (
            <span key={symbol}>
              {index > 0 && " · "}
              <Text span ff="monospace" size="xs" fw={600}>
                {symbol}
              </Text>{" "}
              {meaning}
            </span>
          ))}
        </Text>
      </Stack>
    </Paper>
  );
}

/** "C · concurrency" as a badge, for labels and cards. */
export function QueueTermBadge({ symbol }: { symbol: keyof typeof QUEUE_TERMS }) {
  return (
    <Badge variant="light" color="indigo" size="md" radius="sm" style={{ flexShrink: 0, textTransform: "none" }}>
      {symbol} · {QUEUE_TERMS[symbol].name}
    </Badge>
  );
}
