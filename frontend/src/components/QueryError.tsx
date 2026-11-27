import { Alert } from "@mantine/core";

export function QueryError({ title, error }: { title: string; error: Error }) {
  return (
    <Alert color="red" title={title} role="alert">
      {error.message}
    </Alert>
  );
}
