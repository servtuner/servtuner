import { Anchor, Loader, Table } from "@mantine/core";
import { useApiClient, useRunFiles } from "../../../api/hooks";
import type { RunStatus } from "../../../api/types";
import { formatBytes } from "../../../components/format";
import { QueryError } from "../../../components/QueryError";

export function FilesTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const client = useApiClient();
  const files = useRunFiles(runId, status);
  if (files.isPending) return <Loader size="sm" aria-label="Loading files" />;
  if (files.isError) return <QueryError title="Could not list the bundle's files" error={files.error} />;
  return (
    <Table.ScrollContainer minWidth={420}>
      <Table verticalSpacing={4} style={{ fontVariantNumeric: "tabular-nums" }}>
        <Table.Thead>
          <Table.Tr>
            <Table.Th>File</Table.Th>
            <Table.Th ta="right">Size</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {files.data.map((file) => (
            <Table.Tr key={file.path}>
              <Table.Td>
                <Anchor href={client.fileUrl(runId, file.path)} download size="sm">
                  {file.path}
                </Anchor>
              </Table.Td>
              <Table.Td ta="right">{formatBytes(file.size_bytes)}</Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
