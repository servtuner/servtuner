import { Alert, Code, Loader } from "@mantine/core";
import type { UseQueryResult } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { QueryError } from "../../../components/QueryError";

/**
 * Renders a run document's query: a loader, the error, a note when bench has
 * not analysed the run yet (the API's 404, data null), or the content.
 */
export function WithDocument<T>({
  query,
  what,
  children,
}: {
  query: UseQueryResult<T | null>;
  what: string;
  children: (document: T) => ReactNode;
}) {
  if (query.isPending) return <Loader size="sm" aria-label={`Loading ${what}`} />;
  if (query.isError) return <QueryError title={`Could not load ${what}`} error={query.error} />;
  if (query.data === null) {
    return (
      <Alert variant="light" color="gray" title={`No ${what} yet`}>
        servtuner writes it when it analyses the run, after the run finishes. For a bundle analysed by an older
        servtuner, run <Code>servtuner analyze &lt;bundle&gt;</Code> again.
      </Alert>
    );
  }
  return <>{children(query.data)}</>;
}
