import { Badge, Text, Tooltip } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useApiClient } from "../api/hooks";
import { UI_VERSION, shortCommit, versionsDiffer } from "./version";

/** The running version in the header, and a warning when the UI and the api come from different releases. */
export function VersionBadge({ uiVersion = UI_VERSION }: { uiVersion?: string }) {
  const client = useApiClient();
  // A build does not change while the page is open.
  const { data } = useQuery({ queryKey: ["version"], queryFn: () => client.getVersion(), staleTime: Infinity });
  if (!data) return null;
  const details = `API ${data.version} (${shortCommit(data.commit)}) · UI ${uiVersion}`;
  if (versionsDiffer(uiVersion, data.version))
    return (
      <Tooltip
        label={`${details}. Reload the page; if this stays, the frontend and api images differ.`}
        multiline
        w={280}
      >
        <Badge color="orange" variant="light" style={{ whiteSpace: "nowrap" }}>
          UI {uiVersion} ≠ API {data.version}
        </Badge>
      </Tooltip>
    );
  return (
    <Tooltip label={details}>
      <Text size="xs" c="dimmed" style={{ whiteSpace: "nowrap" }} aria-label={`servtuner version ${data.version}`}>
        v{data.version}
      </Text>
    </Tooltip>
  );
}
