import { Group, Switch, TextInput } from "@mantine/core";

import { type TlsChoice } from "../api/tls";

/** TLS for one hop: on or off, and with it the client certificate and private CA Secrets. */
export function TlsFields({ value, onChange }: { value: TlsChoice; onChange: (value: TlsChoice) => void }) {
  return (
    <Group align="end">
      <Switch
        label="TLS"
        description={
          value.enabled
            ? "The server's certificate is verified"
            : "Plain HTTP: use it where a service mesh or an encrypted network encrypts the connection, or for testing"
        }
        checked={value.enabled}
        onChange={(event) => onChange({ ...value, enabled: event.currentTarget.checked })}
      />
      {value.enabled && (
        <>
          <TextInput
            label="Client certificate Secret"
            description="Optional: a kubernetes.io/tls Secret to present, by name only"
            value={value.clientCertSecret}
            onChange={(event) => onChange({ ...value, clientCertSecret: event.currentTarget.value })}
          />
          <TextInput
            label="CA Secret"
            description="Optional: a Secret whose ca.crt signed the server"
            value={value.caSecret}
            onChange={(event) => onChange({ ...value, caSecret: event.currentTarget.value })}
          />
        </>
      )}
    </Group>
  );
}
