import { Alert, Button, Code, PasswordInput, Stack, Text, TextInput } from "@mantine/core";
import { type FormEvent, useState } from "react";
import { useSetup } from "../../api/auth";
import type { AuthStatus } from "../../api/types";
import { AuthScreen } from "./AuthScreen";
import { authErrorText } from "./errors";

/** The first admin: with local accounts a username and password; with only
 * the proxy, whoever the proxy names on this request. */
export function SetupPage({ status }: { status: AuthStatus }) {
  const setup = useSetup();
  const isLocal = status.methods.includes("local");
  const [setupToken, setSetupToken] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [repeated, setRepeated] = useState("");
  const isMismatch = isLocal && repeated !== "" && repeated !== password;
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (isMismatch) return;
    setup.mutate({ body: isLocal ? { username, password } : {}, setupToken });
  };
  return (
    <AuthScreen
      title="Create the first admin"
      intro={
        <Text size="sm">
          servtuner has no admin yet. Paste the setup token: the chart's install notes show the <Code>kubectl</Code>{" "}
          command that reads it.
        </Text>
      }
    >
      {!isLocal && (
        <Alert color={status.refused_subject ? "blue" : "red"}>
          {status.refused_subject
            ? `You will become the first admin as ${status.refused_subject}.`
            : "This request did not come through the proxy, so there is nobody to make admin."}
        </Alert>
      )}
      <form onSubmit={submit}>
        <Stack gap="sm">
          <PasswordInput
            label="Setup token"
            required
            value={setupToken}
            onChange={(event) => setSetupToken(event.currentTarget.value)}
          />
          {isLocal && (
            <>
              <TextInput
                label="Admin username"
                autoComplete="username"
                required
                value={username}
                onChange={(event) => setUsername(event.currentTarget.value)}
              />
              <PasswordInput
                label="Password"
                description="At least 12 characters"
                autoComplete="new-password"
                required
                value={password}
                onChange={(event) => setPassword(event.currentTarget.value)}
              />
              <PasswordInput
                label="Repeat the password"
                autoComplete="new-password"
                required
                value={repeated}
                error={isMismatch ? "The passwords differ" : undefined}
                onChange={(event) => setRepeated(event.currentTarget.value)}
              />
            </>
          )}
          {setup.error && (
            <Alert color="red" role="alert">
              {authErrorText(setup.error)}
            </Alert>
          )}
          <Button type="submit" loading={setup.isPending} disabled={!isLocal && !status.refused_subject}>
            Create admin
          </Button>
        </Stack>
      </form>
    </AuthScreen>
  );
}
