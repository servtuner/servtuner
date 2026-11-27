import { Badge, Tooltip } from "@mantine/core";

/** The workload preset a run used (RunSpec workload.preset), or nothing for a hand-written workload. */
export function PresetName({ preset }: { preset: string | null | undefined }) {
  if (!preset) return null;
  return (
    <Tooltip label="The workload preset this run used: runs with the same preset ran the same request mix">
      <Badge
        variant="outline"
        color="gray"
        size="sm"
        radius="sm"
        aria-label={`Workload preset ${preset}`}
        styles={{ label: { textTransform: "none", fontFamily: "var(--mantine-font-family-monospace)" } }}
      >
        {preset}
      </Badge>
    </Tooltip>
  );
}
