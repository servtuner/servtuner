import { Alert, Button, PasswordInput, Stack } from "@mantine/core";
import { type FormEvent, useState } from "react";
import { useChangePassword } from "../../api/auth";
import { authErrorText } from "./errors";

/** Needs the current password; the server signs out the user's other sessions. */
export function ChangePasswordForm({ onDone }: { onDone?: () => void }) {
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [repeated, setRepeated] = useState("");
  const isMismatch = repeated !== "" && repeated !== next;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (isMismatch) return;
    change.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: () => {
          setCurrent("");
          setNext("");
          setRepeated("");
          onDone?.();
        },
      },
    );
  };
  return (
    <form onSubmit={submit}>
      <Stack gap="sm" maw={420}>
        <PasswordInput
          label="Current password"
          autoComplete="current-password"
          required
          value={current}
          onChange={(event) => setCurrent(event.currentTarget.value)}
        />
        <PasswordInput
          label="New password"
          description="At least 12 characters"
          autoComplete="new-password"
          required
          value={next}
          onChange={(event) => setNext(event.currentTarget.value)}
        />
        <PasswordInput
          label="Repeat the new password"
          autoComplete="new-password"
          required
          value={repeated}
          error={isMismatch ? "The passwords differ" : undefined}
          onChange={(event) => setRepeated(event.currentTarget.value)}
        />
        {change.error && (
          <Alert color="red" role="alert">
            {authErrorText(change.error)}
          </Alert>
        )}
        {change.isSuccess && <Alert color="green">Password changed; your other sessions were signed out.</Alert>}
        <Button type="submit" loading={change.isPending}>
          Change password
        </Button>
      </Stack>
    </form>
  );
}
