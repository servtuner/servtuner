import { Badge, Text, Tooltip } from "@mantine/core";

/**
 * The model a run tested (RunSpec target.model), set apart so it is never
 * missed: results only mean something for the model they measured.
 */
export function ModelName({ model, size = "sm" }: { model: string | null | undefined; size?: "sm" | "lg" }) {
  if (!model) {
    return (
      <Text size="sm" c="dimmed" span>
        –
      </Text>
    );
  }
  return (
    <Tooltip label="The model this run tested (target.model)">
      <Badge
        variant="light"
        color="indigo"
        size={size}
        radius="sm"
        aria-label={`Model ${model}`}
        styles={{ label: { textTransform: "none", fontFamily: "var(--mantine-font-family-monospace)" } }}
      >
        {model}
      </Badge>
    </Tooltip>
  );
}
