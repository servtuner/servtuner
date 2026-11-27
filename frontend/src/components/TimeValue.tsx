// Viewer-local time on screen, the UTC value on hover (frontend/CLAUDE.md).
export function TimeValue({ timestamp }: { timestamp: string | null | undefined }) {
  if (timestamp === null || timestamp === undefined) return <>-</>;
  const instant = new Date(timestamp);
  if (Number.isNaN(instant.getTime())) return <>{timestamp}</>;
  return (
    <time dateTime={instant.toISOString()} title={`${instant.toISOString()} (UTC)`}>
      {instant.toLocaleString()}
    </time>
  );
}
