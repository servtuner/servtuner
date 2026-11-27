import { Alert, List, Loader, Stack, Text } from "@mantine/core";
import type { ReactNode } from "react";
import { QueryError } from "./QueryError";

/**
 * The body of a delete confirmation: what blocks the delete and how to free it,
 * what goes with it, and what is kept but changes. The api computes all of it; this only lists it.
 */
export function DeleteImpact({
  isPending,
  error,
  blocks,
  goes,
  notes,
  children,
}: {
  isPending: boolean;
  error: Error | null;
  blocks: string[];
  goes: string[];
  notes: string[];
  children?: ReactNode;
}) {
  if (isPending) return <Loader size="sm" aria-label="Checking what a delete would remove" />;
  if (error) return <QueryError title="Could not check what a delete would remove" error={error} />;
  return (
    <Stack gap="sm">
      {blocks.length > 0 && (
        <Alert color="red" title="It cannot be deleted yet">
          <List size="sm">
            {blocks.map((block) => (
              <List.Item key={block}>{block}</List.Item>
            ))}
          </List>
        </Alert>
      )}
      <Text size="sm">This deletes, and it cannot be undone:</Text>
      <List size="sm">
        {goes.map((item) => (
          <List.Item key={item}>{item}</List.Item>
        ))}
      </List>
      {notes.map((note) => (
        <Text key={note} size="sm" c="dimmed">
          {note}
        </Text>
      ))}
      {children}
    </Stack>
  );
}
