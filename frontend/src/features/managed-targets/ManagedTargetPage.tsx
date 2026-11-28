import {
  Accordion,
  Alert,
  Anchor,
  Badge,
  Button,
  Code,
  Group,
  Loader,
  Paper,
  Stack,
  Table,
  Text,
  Title,
} from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import {
  useHosts,
  useKnobsByName,
  useManagedTarget,
  useManagedTargetActions,
  useManagedTargetReadiness,
  useSaveManagedTarget,
} from "../../api/hooks";
import type { KnobDescription, ManagedTarget, ManagedTargetReadiness, VllmKnobs } from "../../api/types";
import { CheckResults } from "../../components/CheckResults";
import { ConfirmButton } from "../../components/ConfirmButton";
import { QueryError } from "../../components/QueryError";
import { TimeValue } from "../../components/TimeValue";
import { KnobEditor } from "../knobs/KnobEditor";
import { hardwareLine, priceLine } from "../hosts/summary";
import { overriddenLine } from "../knobs/baseFlags";
import { entriesOf, knobsOf, type KnobEntry } from "../knobs/knobValues";
import { LauncherCheckPanel } from "./LauncherCheckPanel";
import { LogPanel } from "./LogPanel";
import { TargetRuns } from "./TargetRuns";
import { ManagedTargetForm } from "./ManagedTargetForm";
import { placementLine } from "./summary";

/**
 * One managed vLLM: what servtuner deploys, whether it booted, and
 * deploy, scale down and delete, so bringing a vLLM up never needs kubectl or curl.
 */
export function ManagedTargetPage() {
  const { targetId = "" } = useParams();
  const target = useManagedTarget(targetId);
  if (target.isPending) return <Loader size="sm" aria-label="Loading the managed target" />;
  if (target.isError) return <QueryError title="Could not load the managed target" error={target.error} />;
  return <ManagedTargetView target={target.data} />;
}

function ManagedTargetView({ target }: { target: ManagedTarget }) {
  const actions = useManagedTargetActions(target.id);
  const readiness = useManagedTargetReadiness(target.id, Boolean(target.deployment));
  const navigate = useNavigate();
  const failedAction = [actions.scaleDown, actions.remove].find((action) => action.isError);
  return (
    <Stack gap="lg">
      <Anchor component={Link} to="/managed-targets" size="sm">
        All managed vLLMs
      </Anchor>
      <Group justify="space-between" align="flex-start" gap="md">
        <div>
          <Title order={2}>{target.name}</Title>
          <Text c="dimmed" size="sm">
            {placementLine(target)} · model{" "}
            <Text span ff="monospace">
              {target.spec.model}
            </Text>
          </Text>
          <Text c="dimmed" size="sm">
            Runs reach it at{" "}
            <Text span ff="monospace">
              {target.run_target.endpoint}
            </Text>
          </Text>
          {target.spec.placement === "host" && <RunsOn hostId={target.spec.host_id ?? null} />}
          {target.description && <Text size="sm">{target.description}</Text>}
        </div>
        <Group gap="xs">
          <Button component={Link} to={`/runs/new?managed=${target.id}`}>
            Benchmark
          </Button>
          <Button component={Link} variant="light" to={`/tuning/new?target=${target.id}`}>
            Tune
          </Button>
          <Button
            variant="default"
            disabled={!target.deployment}
            loading={actions.scaleDown.isPending}
            onClick={() => actions.scaleDown.mutate()}
          >
            {target.spec.placement === "host" ? "Stop vLLM" : "Scale to zero"}
          </Button>
          <ConfirmButton
            label="Delete managed target"
            title={`Delete ${target.name}?`}
            message={
              "servtuner tears down what it deployed for it (the model cache stays) and forgets the target. " +
              "Past runs and sessions are kept."
            }
            confirmLabel="Delete"
            isPending={actions.remove.isPending}
            onConfirm={() => actions.remove.mutate(undefined, { onSuccess: () => navigate("/managed-targets") })}
          />
        </Group>
      </Group>
      {failedAction?.error && (
        <Alert color="red" title="That did not work">
          {failedAction.error.message}
        </Alert>
      )}

      <Stack gap="sm">
        <Title order={4}>Status</Title>
        {target.spec.placement === "host" && <LauncherCheckPanel check={actions.checkLauncher} />}
        {target.deployment ? (
          <ReadinessPanel
            readiness={readiness.data}
            isChecking={readiness.isFetching}
            error={readiness.error}
            onRecheck={() => readiness.refetch()}
          />
        ) : (
          <Text size="sm" c="dimmed">
            Not deployed yet.
          </Text>
        )}
      </Stack>

      {target.deployment && <LogPanel targetId={target.id} isBooting={readiness.data?.state === "pending"} />}

      <DeployForm
        target={target}
        isPending={actions.deploy.isPending}
        error={actions.deploy.error?.message}
        onDeploy={(knobs) => actions.deploy.mutate(knobs)}
      />

      <TargetRuns target={target} />

      <Details target={target} />
    </Stack>
  );
}

const READINESS_BADGE: Record<ManagedTargetReadiness["state"], { label: string; color: string }> = {
  pending: { label: "Starting", color: "blue" },
  ready: { label: "Ready", color: "teal" },
  infeasible: { label: "Cannot boot with these flags", color: "orange" },
  failed: { label: "Failed", color: "red" },
};

function ReadinessPanel({
  readiness,
  isChecking,
  error,
  onRecheck,
}: {
  readiness: ManagedTargetReadiness | undefined;
  isChecking: boolean;
  error: Error | null;
  onRecheck: () => void;
}) {
  if (error) return <QueryError title="Could not check the deployment" error={error} />;
  if (!readiness) return <Loader size="sm" aria-label="Checking the deployment" />;
  const badge = READINESS_BADGE[readiness.state];
  return (
    <Stack gap="xs">
      <Group gap="sm">
        <Badge variant="light" color={badge.color}>
          {badge.label}
        </Badge>
        <Text size="sm">{readiness.reason}</Text>
        {readiness.state !== "pending" && (
          <Button variant="subtle" size="compact-sm" loading={isChecking} onClick={onRecheck}>
            Check again
          </Button>
        )}
      </Group>
      {readiness.failure && (
        <Stack gap={4}>
          <Text size="sm">
            {readiness.failure.message}{" "}
            <Text span c="dimmed">
              ({readiness.failure.rule})
            </Text>
          </Text>
          {readiness.failure.excerpt.length > 0 && (
            <Code block aria-label="vLLM log excerpt">
              {readiness.failure.excerpt.join("\n")}
            </Code>
          )}
        </Stack>
      )}
      {readiness.check && <CheckResults view={readiness.check} />}
    </Stack>
  );
}

function DeployForm({
  target,
  isPending,
  error,
  onDeploy,
}: {
  target: ManagedTarget;
  isPending: boolean;
  error: string | undefined;
  onDeploy: (knobs: VllmKnobs) => void;
}) {
  const [entries, setEntries] = useState(() => entriesOf(target.deployment?.knobs));
  const { query: knobCatalog, catalog } = useKnobsByName(
    target.spec.placement === "kubernetes" ? (target.spec.server ?? "vllm") : "vllm",
  );
  const knobs = knobsOf(entries, catalog);
  return (
    <Stack gap="sm">
      <Title order={4}>Deploy</Title>
      {target.deployment && (
        <Stack gap={4}>
          <Text size="sm">
            Last deployed <TimeValue timestamp={target.deployment.deployed_at} /> with:
          </Text>
          <Code block aria-label="Container args">
            {target.deployment.args.join(" ")}
          </Code>
        </Stack>
      )}
      {knobCatalog.isError && <QueryError title="Could not load the knob catalog" error={knobCatalog.error} />}
      <KnobEditor
        label="vLLM knobs"
        description="None: vLLM's defaults plus the base flags."
        entries={entries}
        catalog={catalog}
        onChange={setEntries}
      />
      <Overrides entries={entries} catalog={catalog} baseFlags={target.spec.base_flags ?? []} />
      <Group>
        <Button loading={isPending} disabled={knobs === null} onClick={() => knobs && onDeploy(knobs)}>
          {target.deployment ? "Redeploy" : "Deploy"}
        </Button>
        <Text size="xs" c="dimmed">
          A redeploy restarts vLLM (one replica, Recreate), so a run against it fails.
        </Text>
      </Group>
      {error && (
        <Alert color="red" title="Not deployed">
          {error}
        </Alert>
      )}
    </Stack>
  );
}

function Details({ target }: { target: ManagedTarget }) {
  const update = useSaveManagedTarget(target.id);
  const [isEditing, setIsEditing] = useState(false);
  const [isOpen, setIsOpen] = useState(false);
  const spec = target.spec;
  const rows: [string, React.ReactNode][] =
    spec.placement === "host"
      ? [
          ["Launcher", spec.launcher_url],
          ["Endpoint", spec.endpoint],
          ["Metrics namespace label", target.namespace],
        ]
      : [
          ["Namespace", target.namespace],
          ["Image", spec.image],
          ["GPUs", spec.gpus],
          ["CPU and memory", `${spec.cpu}, ${spec.memory}`],
          ["Shared memory (/dev/shm)", spec.shm_size ?? "Unlimited (counts against memory)"],
          [
            "Weights",
            spec.model_source?.kind === "pvc"
              ? `Seeded claim ${spec.model_source.claim} (offline)`
              : spec.model_source?.kind === "object_store"
                ? `Streamed from ${spec.model_source.uri}`
                : spec.model_source?.kind === "mirror"
                  ? `From ${spec.model_source.endpoint}, cached in ${spec.model_cache?.size ?? "?"}`
                  : `From Hugging Face, cached in ${spec.model_cache?.size ?? "?"}`,
          ],
          ["Transport", spec.transport?.enabled ? "TLS (its own certificate)" : "Plain HTTP"],
        ];
  rows.push(["Base flags", spec.base_flags?.length ? spec.base_flags.join(" ") : "–"]);
  return (
    // Folded: what servtuner deploys matters when editing it, not on every visit. Editing keeps it open.
    <Accordion
      variant="contained"
      maw={900}
      value={isOpen || isEditing ? "details" : null}
      onChange={(value) => setIsOpen(value === "details")}
    >
      <Accordion.Item value="details">
        <Accordion.Control>Details</Accordion.Control>
        <Accordion.Panel>
          <Stack gap="sm">
            {!isEditing && (
              <Group justify="flex-end">
                <Button variant="default" size="xs" onClick={() => setIsEditing(true)}>
                  Edit
                </Button>
              </Group>
            )}
            {isEditing ? (
              <Paper withBorder p="md" radius="sm">
                <ManagedTargetForm
                  existing={target}
                  submitLabel="Save changes"
                  isPending={update.isPending}
                  error={update.error?.message}
                  onSubmit={(save) => update.mutate(save, { onSuccess: () => setIsEditing(false) })}
                  onCancel={() => setIsEditing(false)}
                />
                <Text size="xs" c="dimmed" mt="sm">
                  Saving changes nothing that is running: the next deploy uses them.
                </Text>
              </Paper>
            ) : (
              <Table variant="vertical" withTableBorder maw={900}>
                <Table.Tbody>
                  {rows.map(([label, value]) => (
                    <Table.Tr key={label}>
                      <Table.Th w={220}>{label}</Table.Th>
                      <Table.Td ff={typeof value === "string" ? "monospace" : undefined}>{value}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            )}
          </Stack>
        </Accordion.Panel>
      </Accordion.Item>
    </Accordion>
  );
}

/** Knobs set on top of a base flag for the same field: the knob comes last, so vLLM takes it. */
function Overrides({
  entries,
  catalog,
  baseFlags,
}: {
  entries: KnobEntry[];
  catalog: Map<string, KnobDescription>;
  baseFlags: string[];
}) {
  const overrides = entries.flatMap((entry) => {
    const knob = catalog.get(entry.name);
    const line = knob && overriddenLine(baseFlags, knob);
    return knob && line ? [{ label: knob.label, line }] : [];
  });
  if (overrides.length === 0) return null;
  return (
    <Text size="xs" c="dimmed" aria-label="Overridden base flags">
      {overrides.map(({ label, line }, index) => (
        <span key={line}>
          {index > 0 && "; "}
          {label} overrides the base flag <Code>{line}</Code>
        </span>
      ))}
    </Text>
  );
}

/** The machine a host vLLM runs on, its hardware and price, or that none is named. */
function RunsOn({ hostId }: { hostId: string | null }) {
  const hosts = useHosts();
  const host = hosts.data?.find((candidate) => candidate.id === hostId);
  if (!hostId)
    return (
      <Text c="dimmed" size="sm">
        No host named: edit the target to pick the machine it runs on, with its hardware and price.
      </Text>
    );
  if (!host) return null;
  return (
    <Text size="sm" aria-label="Runs on">
      Runs on{" "}
      <Anchor component={Link} to={`/hosts/${host.id}`}>
        {host.name}
      </Anchor>
      : {hardwareLine(host.hardware)} · {priceLine({ usd_per_hour: host.usd_per_hour, price_basis: null })}
    </Text>
  );
}
