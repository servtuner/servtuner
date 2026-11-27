import {
  Badge,
  Button,
  Code,
  Group,
  Paper,
  PasswordInput,
  SegmentedControl,
  Stack,
  Text,
  TextInput,
} from "@mantine/core";
import { useState } from "react";
import type { CredentialInput, CredentialStatus } from "../../api/types";
import { TimeValue } from "../../components/TimeValue";

type Form = "managed" | "basic" | "external";

const SLOT_TITLES: Record<string, string> = {
  api_key: "API key",
  admin: "Admin credential",
};

const SLOT_HELP: Record<string, string> = {
  api_key: "Sent as Authorization: Bearer to the target, for every request of a run.",
  admin: "For the target's config API (Bifrost /api/config), when it differs from the API key.",
};

/** What is stored, never the value: a masked preview, or the Secret a user manages. */
function StatusLine({ status }: { status: CredentialStatus }) {
  if (!status.is_set) {
    return (
      <Badge color="gray" variant="light">
        Not set
      </Badge>
    );
  }
  return (
    <Group gap="xs" wrap="wrap">
      <Badge color="teal" variant="light">
        Set
      </Badge>
      {status.source === "external" ? (
        <Text size="sm">
          from your Secret <Code>{status.secret_name}</Code>
        </Text>
      ) : (
        <>
          {status.username && <Text size="sm">user {status.username},</Text>}
          <Code aria-label="Masked key">{status.preview}</Code>
        </>
      )}
      {status.set_at && (
        <Text size="xs" c="dimmed">
          set <TimeValue timestamp={status.set_at} />
        </Text>
      )}
    </Group>
  );
}

/**
 * One write-only credential slot. The typed key is sent
 * only by Test key (used once, never stored) or Set key, and cleared from the
 * page as soon as it is set.
 */
export function CredentialCard({
  slot,
  status,
  onSet,
  onClear,
  onTest,
  isSetting,
  isTesting,
  error,
}: {
  slot: string;
  status: CredentialStatus;
  onSet: (body: CredentialInput, done: () => void) => void;
  onClear: () => void;
  onTest?: (key: string) => void;
  isSetting: boolean;
  isTesting: boolean;
  error?: string;
}) {
  const [form, setForm] = useState<Form>("managed");
  const [value, setValue] = useState("");
  const [username, setUsername] = useState("");
  const [secretName, setSecretName] = useState("");
  const [secretKey, setSecretKey] = useState(slot === "admin" ? "" : "token");
  const [usernameKey, setUsernameKey] = useState("");
  const [passwordKey, setPasswordKey] = useState("");
  const clear = () => {
    setValue("");
    setUsername("");
  };

  const body = (): CredentialInput => {
    if (form === "external") {
      if (slot === "api_key") return { external: { name: secretName, key: secretKey } };
      return usernameKey
        ? { external: { name: secretName, username_key: usernameKey, password_key: passwordKey } }
        : { external: { name: secretName, token_key: secretKey } };
    }
    if (form === "basic") return { username, password: value };
    return { value };
  };
  const ready = form === "external" ? secretName.trim() !== "" : value !== "" && (form !== "basic" || username !== "");
  const forms = [
    { value: "managed", label: slot === "admin" ? "Token" : "Key" },
    ...(slot === "admin" ? [{ value: "basic", label: "Username and password" }] : []),
    { value: "external", label: "A Secret I manage" },
  ];

  return (
    <Paper withBorder p="md" radius="sm">
      <Stack gap="sm">
        <Group justify="space-between" align="start">
          <div>
            <Text fw={600}>{SLOT_TITLES[slot] ?? slot}</Text>
            <Text size="xs" c="dimmed">
              {SLOT_HELP[slot]}
            </Text>
          </div>
          <StatusLine status={status} />
        </Group>
        <SegmentedControl size="xs" value={form} onChange={(next) => setForm(next as Form)} data={forms} />
        {form === "basic" && (
          <TextInput label="Username" value={username} onChange={(e) => setUsername(e.currentTarget.value)} />
        )}
        {form !== "external" ? (
          <PasswordInput
            label={form === "basic" ? "Password" : `New ${SLOT_TITLES[slot]?.toLowerCase() ?? "key"}`}
            description="Write-only: servtuner stores it in a Kubernetes Secret and never shows it again."
            autoComplete="new-password"
            value={value}
            onChange={(e) => setValue(e.currentTarget.value)}
          />
        ) : (
          <Group grow align="start">
            <TextInput label="Secret name" value={secretName} onChange={(e) => setSecretName(e.currentTarget.value)} />
            {slot === "api_key" || !usernameKey ? (
              <TextInput
                label={slot === "api_key" ? "Key" : "Token key"}
                value={secretKey}
                onChange={(e) => setSecretKey(e.currentTarget.value)}
              />
            ) : null}
            {slot === "admin" && (
              <>
                <TextInput
                  label="Username key"
                  value={usernameKey}
                  onChange={(e) => setUsernameKey(e.currentTarget.value)}
                />
                <TextInput
                  label="Password key"
                  value={passwordKey}
                  onChange={(e) => setPasswordKey(e.currentTarget.value)}
                />
              </>
            )}
          </Group>
        )}
        {error && (
          <Text size="sm" c="red" role="alert">
            {error}
          </Text>
        )}
        <Group>
          {onTest && form === "managed" && (
            <Button variant="default" disabled={value === ""} loading={isTesting} onClick={() => onTest(value)}>
              Test key
            </Button>
          )}
          <Button disabled={!ready} loading={isSetting} onClick={() => onSet(body(), clear)}>
            {status.is_set ? "Replace" : "Set"} {form === "external" ? "Secret" : "key"}
          </Button>
          {status.is_set && (
            <Button variant="subtle" color="red" onClick={onClear}>
              Remove
            </Button>
          )}
        </Group>
      </Stack>
    </Paper>
  );
}
