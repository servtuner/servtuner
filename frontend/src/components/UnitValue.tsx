import { Text } from "@mantine/core";
import { formatNumber, scaleBytes } from "./format";

/**
 * A value with its unit dimmed, as derivation tables show it. bench sends
 * memory in bytes; it is shown in KiB, MiB, GiB like every other byte value.
 * Other units pass through.
 */
export function UnitValue({ value, unit }: { value: number | null | undefined; unit?: string | null }) {
  const shown = unit === "bytes" && value != null ? scaleBytes(value) : { value: formatNumber(value), unit };
  return (
    <Text size="sm">
      {shown.value}
      {shown.unit && (
        <Text span size="xs" c="dimmed">
          {" "}
          {shown.unit}
        </Text>
      )}
    </Text>
  );
}
