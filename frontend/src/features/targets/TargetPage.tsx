import { Alert, Anchor, Button, Checkbox, Group, Loader, Paper, Stack, Text, Title } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useHosts, useTarget, useTargetActions } from "../../api/hooks";
import type { CredentialStatus, TargetCheckView } from "../../api/types";
import { CheckResults } from "../../components/CheckResults";
import { ConfirmButton } from "../../components/ConfirmButton";
import { QueryError } from "../../components/QueryError";
import { CredentialCard } from "./CredentialCard";
import { TargetForm } from "./TargetForm";
import { pricingLine } from "./pricing";

const UNSET: CredentialStatus = { is_set: false };

/** One saved target: Check connection, its write-only keys, its details. */
export function TargetPage() {
  const { targetId = "" } = useParams();
  const target = useTarget(targetId);
  const hosts = useHosts();
  const actions = useTargetActions(targetId);
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [generate, setGenerate] = useState(false);
  // The latest check shown: a one-time key's result is shown but never stored.
  const [shown, setShown] = useState<TargetCheckView | undefined>();

  if (target.isPending) return <Loader size="sm" aria-label="Loading the target" />;
  if (target.isError) return <QueryError title="Could not load the target" error={target.error} />;
  const saved = target.data;
  const check = shown ?? saved.last_check ?? undefined;

  return (
    <Stack gap="lg">
      <Anchor component={Link} to="/targets" size="sm">
        All targets
      </Anchor>
      <div>
        <Title order={2}>{saved.name}</Title>
        <Text c="dimmed" size="sm">
          {saved.type} ·{" "}
          <Text span ff="monospace">
            {saved.target.endpoint}
          </Text>{" "}
          · model {saved.target.model}
        </Text>
        {saved.description && <Text size="sm">{saved.description}</Text>}
        <Text size="sm" mt={4}>
          {pricingLine(saved, (id) => hosts.data?.find((host) => host.id === id)?.name)}
        </Text>
        {(saved.pricing_problems ?? []).map((problem) => (
          <Alert key={problem} color="yellow" mt="xs" title="Its runs are not priced now">
            {problem}
          </Alert>
        ))}
      </div>

      <Stack gap="sm">
        <Title order={4}>Connection</Title>
        <Group>
          <Button
            loading={actions.check.isPending && !actions.check.variables?.api_key}
            onClick={() => actions.check.mutate({ generate }, { onSuccess: (view) => setShown(view) })}
          >
            Check connection
          </Button>
          <Checkbox
            label="Also send one test request (costs a request on a paid API)"
            checked={generate}
            onChange={(event) => setGenerate(event.currentTarget.checked)}
          />
        </Group>
        {actions.check.isError && (
          <Text size="sm" c="red" role="alert">
            {actions.check.error.message}
          </Text>
        )}
        {check && <CheckResults view={check} />}
      </Stack>

      <Stack gap="sm">
        <Title order={4}>Credentials</Title>
        {saved.slots.map((slot) => (
          <CredentialCard
            key={slot}
            slot={slot}
            status={saved.credentials[slot] ?? UNSET}
            isSetting={actions.setCredential.isPending && actions.setCredential.variables?.slot === slot}
            isTesting={actions.check.isPending && Boolean(actions.check.variables?.api_key)}
            error={
              actions.setCredential.isError && actions.setCredential.variables?.slot === slot
                ? actions.setCredential.error.message
                : undefined
            }
            onSet={(body, done) => actions.setCredential.mutate({ slot, body }, { onSuccess: done })}
            onClear={() => actions.clearCredential.mutate(slot)}
            onTest={
              slot === "api_key"
                ? (key) => actions.check.mutate({ api_key: key, generate }, { onSuccess: (view) => setShown(view) })
                : undefined
            }
          />
        ))}
      </Stack>

      <Stack gap="sm">
        <Group justify="space-between">
          <Title order={4}>Details</Title>
          {!editing && (
            <Button variant="default" size="xs" onClick={() => setEditing(true)}>
              Edit
            </Button>
          )}
        </Group>
        {editing ? (
          <Paper withBorder p="md" radius="sm">
            <TargetForm
              existing={saved}
              submitLabel="Save changes"
              isPending={actions.update.isPending}
              error={actions.update.error?.message}
              onCancel={() => setEditing(false)}
              onSubmit={(body) => actions.update.mutate(body, { onSuccess: () => setEditing(false) })}
            />
          </Paper>
        ) : (
          <Text size="sm" c="dimmed">
            New run copies this target block into the RunSpec, so editing it later never changes a past run.
          </Text>
        )}
      </Stack>

      <Group>
        <ConfirmButton
          label="Delete target"
          title={`Delete ${saved.name}?`}
          message={
            "The keys servtuner manages for it are deleted too. " +
            "Past runs keep their copy of the target but cannot be rerun until a key is set again."
          }
          confirmLabel="Delete"
          isPending={actions.remove.isPending}
          onConfirm={() => actions.remove.mutate(undefined, { onSuccess: () => navigate("/targets") })}
        />
      </Group>
    </Stack>
  );
}
