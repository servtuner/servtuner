import { Text, Timeline } from "@mantine/core";
import type { RunEvent } from "../../api/types";
import { TimeValue } from "../../components/TimeValue";

export function StatusTimeline({ events }: { events: RunEvent[] }) {
  if (events.length === 0) return <Text c="dimmed">No events reported yet.</Text>;
  const ordered = [...events].sort((a, b) => a.sequence - b.sequence);
  return (
    <Timeline active={ordered.length - 1} bulletSize={14} lineWidth={2}>
      {ordered.map((event) => (
        <Timeline.Item
          key={event.sequence}
          title={`${event.step}: ${event.state}${event.phase ? ` (${event.phase})` : ""}`}
        >
          {event.message && <Text size="sm">{event.message}</Text>}
          <Text size="xs" c="dimmed">
            <TimeValue timestamp={event.time} />
          </Text>
        </Timeline.Item>
      ))}
    </Timeline>
  );
}
