import { Alert, Button, PasswordInput, Stack, TextInput } from "@mantine/core";
import { type FormEvent, useState } from "react";
import { useLogin } from "../../api/auth";
import { AuthScreen } from "./AuthScreen";
import { authErrorText } from "./errors";

export function LoginPage({ refusedSubject }: { refusedSubject?: string | null }) {
  const login = useLogin();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    login.mutate({ username, password });
  };
  return (
    <AuthScreen title="Sign in">
      {refusedSubject && (
        <Alert color="yellow" title="Your proxy identity has no account">
          {refusedSubject} is not linked to a servtuner user; sign in with a password, or ask an admin to link it.
        </Alert>
      )}
      <form onSubmit={submit}>
        <Stack gap="sm">
          <TextInput
            label="Username"
            autoComplete="username"
            required
            value={username}
            onChange={(event) => setUsername(event.currentTarget.value)}
          />
          <PasswordInput
            label="Password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.currentTarget.value)}
          />
          {login.error && (
            <Alert color="red" role="alert">
              {authErrorText(login.error)}
            </Alert>
          )}
          <Button type="submit" loading={login.isPending}>
            Sign in
          </Button>
        </Stack>
      </form>
    </AuthScreen>
  );
}
