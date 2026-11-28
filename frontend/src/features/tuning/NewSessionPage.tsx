import {
  Alert,
  Anchor,
  Button,
  Group,
  NumberInput,
  SegmentedControl,
  Select,
  SimpleGrid,
  Stack,
  Text,
  TextInput,
  Textarea,
  Title,
} from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { parse } from "yaml";
import {
  useCreateTuningSession,
  useKnobsByName,
  useManagedTargets,
  useSearchSpaces,
  useWorkloadPresets,
} from "../../api/hooks";
import type { SearchSpaceTemplate } from "../../api/types";
import { FIELD_GRID_STYLE } from "../../components/layout";
import { QueryError } from "../../components/QueryError";
import { KnobEditor } from "../knobs/KnobEditor";
import { entriesOf, knobsOf } from "../knobs/knobValues";
import { presetReference } from "../new-run/presets";
import { ADD_HOST_FOR_TUNING } from "./session";
import { ProbeRates } from "./ProbeRates";
import { ReuseSessions } from "./ReuseSessions";
import { SpaceKnobEditor } from "./SpaceKnobEditor";
import { baseOf, tunedKnobsOf, tunedProblem, withBase, withTunedKnobs } from "./spaceKnobs";
import { rankForTarget, templateKey, templateText, withObjective, withProbeRates } from "./spaces";
import { OBJECTIVE_LABELS } from "../experiments/objectives";
import type { ObjectiveName } from "../../api/types";

const DEFAULT_MAX_RUNS = 40;
const DEFAULT_MAX_HOURS = 8;
const SLO_FIELDS = [
  ["ttft_p95", "TTFT p95"],
  ["itl_p95", "ITL p95"],
  ["e2e_p95", "E2E p95"],
] as const;

/** Start a tuning session: a managed target, a preset, SLOs, a space and a budget. */
export function NewSessionPage() {
  const targets = useManagedTargets();
  const presets = useWorkloadPresets();
  const create = useCreateTuningSession();
  const navigate = useNavigate();
  // ?target=<id>: the managed server page's Tune button picks the target.
  const [searchParams] = useSearchParams();
  const [targetId, setTargetId] = useState<string | null>(searchParams.get("target"));
  const [preset, setPreset] = useState<string | null>(null);
  const [slos, setSlos] = useState<Record<string, string>>({});
  const spaces = useSearchSpaces();
  // The space shown: the user's edit, else the starting space they picked, else the best fit.
  const [pickedTemplate, setPickedTemplate] = useState<string | null>(null);
  const [editedSpace, setEditedSpace] = useState<string | null>(null);
  const [isEditingSpace, setIsEditingSpace] = useState(false);
  const target = targets.data?.find((candidate) => candidate.id === targetId);
  const { fitting, others } = rankForTarget(spaces.data ?? [], target);
  const template =
    [...fitting, ...others].find((candidate) => templateKey(candidate) === pickedTemplate) ?? fitting[0] ?? others[0];
  const space = editedSpace ?? (template ? templateText(template) : "");
  const [reused, setReused] = useState<string[]>([]);
  const [maxRuns, setMaxRuns] = useState<number>(DEFAULT_MAX_RUNS);
  const [maxHours, setMaxHours] = useState<number>(DEFAULT_MAX_HOURS);
  const [mode, setMode] = useState<"auto" | "manual">("auto");
  let spaceError: string | null = null;
  let parsedSpace: Record<string, unknown> = {};
  try {
    parsedSpace = (parse(space) ?? {}) as Record<string, unknown>;
  } catch (error) {
    spaceError = (error as Error).message;
  }
  const targetServer = target?.spec.placement === "kubernetes" ? (target.spec.server ?? "vllm") : "vllm";
  const { query: knobCatalog, catalog } = useKnobsByName(targetServer);
  const tuned = tunedKnobsOf(parsedSpace);
  const base = entriesOf(baseOf(parsedSpace));
  const hasKnobProblem =
    knobsOf(base, catalog) === null || tuned.some((knob) => tunedProblem(knob, catalog.get(knob.name)) !== null);
  const choosePreset = (reference: string | null) => {
    setPreset(reference);
    setReused([]); // reuse needs the same preset
    const chosen = presets.data?.find((candidate) => presetReference(candidate) === reference);
    if (chosen) {
      setSlos(
        Object.fromEntries(
          Object.entries(chosen.slos)
            .filter(([, value]) => value != null)
            .map(([key, value]) => [key, String(value)]),
        ),
      );
    }
  };
  const submit = () =>
    targetId &&
    preset &&
    create.mutate(
      {
        managed_target_id: targetId,
        preset,
        slos: Object.fromEntries(Object.entries(slos).filter(([, value]) => value.trim())),
        space: parsedSpace,
        budget: { max_runs: maxRuns, max_hours: maxHours },
        mode,
        ...(reused.length > 0 ? { reuse_from: reused } : {}),
      },
      { onSuccess: (session) => navigate(`/tuning/${session.id}`) },
    );
  return (
    <Stack gap="md" maw={900}>
      <Anchor component={Link} to="/tuning" size="sm">
        Tuning
      </Anchor>
      <Title order={2}>New tuning session</Title>
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <Select
          description={
            <>
              A vLLM servtuner deploys.{" "}
              <Anchor component={Link} to={ADD_HOST_FOR_TUNING} size="xs">
                Add a vLLM host
              </Anchor>{" "}
              (servtuner launcher)
            </>
          }
          label="Managed target"
          data={(targets.data ?? []).map((target) => ({
            value: target.id,
            label: `${target.name} (${target.spec.model})`,
          }))}
          value={targetId}
          onChange={(id) => {
            setTargetId(id);
            setReused([]); // reuse needs the same target
          }}
          required
        />
        <Select
          label="Workload preset"
          description="The request mix the flags are tuned for"
          data={(presets.data ?? []).map((candidate) => presetReference(candidate))}
          value={preset}
          onChange={choosePreset}
          required
        />
      </SimpleGrid>
      <Group>
        {SLO_FIELDS.map(([key, label]) => (
          <TextInput
            key={key}
            label={label}
            value={slos[key] ?? ""}
            onChange={(event) => {
              const value = event.currentTarget.value;
              setSlos((current) => ({ ...current, [key]: value }));
            }}
            w={160}
          />
        ))}
      </Group>
      <Select
        label="Start from"
        description={
          template ? template.description : "A starting search space from servtuner's catalog; edit it below as needed"
        }
        placeholder={spaces.isError ? "The catalog did not load: write the space below" : undefined}
        data={[
          ...(fitting.length > 0
            ? [{ group: `Fits ${target?.name ?? "this target"}`, items: fitting.map(templateOption) }]
            : []),
          ...(others.length > 0
            ? [{ group: target ? "Other starting spaces" : "Starting spaces", items: others.map(templateOption) }]
            : []),
        ]}
        value={template ? templateKey(template) : null}
        onChange={(key) => {
          setPickedTemplate(key);
          setEditedSpace(null); // a new start replaces the edits
        }}
        allowDeselect={false}
      />
      {target && (target.session_objectives?.length ?? 0) > 0 && (
        <Select
          label="Rank by"
          description="What the session optimizes. Per-GPU objectives need the host's hardware, cost ones its price (Hosts)."
          data={(target.session_objectives ?? []).map((objective) => ({
            value: objective,
            label: OBJECTIVE_LABELS[objective as ObjectiveName] ?? objective,
          }))}
          value={typeof parsedSpace.objective === "string" ? parsedSpace.objective : target.session_objectives![0]!}
          onChange={(objective) => objective && setEditedSpace(withObjective(space, objective))}
          allowDeselect={false}
          w={360}
        />
      )}
      {isEditingSpace || spaceError ? (
        <Textarea
          label="Search space"
          description="YAML. servtuner propose checks it when the session starts."
          value={space}
          onChange={(event) => setEditedSpace(event.currentTarget.value)}
          autosize
          minRows={8}
          spellCheck={false}
          error={spaceError}
          styles={{
            input: {
              fontFamily: "var(--mantine-font-family-monospace)",
              fontSize: 13,
            },
          }}
        />
      ) : (
        // Most sessions start from the picked space; its knobs are chips, the rest stays in the YAML.
        <Stack gap="sm">
          {knobCatalog.isError && <QueryError title="Could not load the knob catalog" error={knobCatalog.error} />}
          <SpaceKnobEditor
            knobs={tuned}
            catalog={catalog}
            exclude={new Set(base.map((entry) => entry.name))}
            onChange={(knobs) => setEditedSpace(withTunedKnobs(space, knobs))}
          />
          <KnobEditor
            label="Fixed knobs"
            description="Set on every configuration and never tuned"
            entries={base}
            catalog={catalog}
            exclude={new Set(tuned.map((knob) => knob.name))}
            onChange={(entries) =>
              setEditedSpace(withBase(space, Object.fromEntries(entries.map((entry) => [entry.name, entry.value]))))
            }
          />
          <Group>
            <Button variant="subtle" size="compact-sm" onClick={() => setIsEditingSpace(true)}>
              Edit as YAML
            </Button>
          </Group>
        </Stack>
      )}
      {target && <ReuseSessions target={target} preset={preset} reused={reused} onReusedChange={setReused} />}
      {target && (
        <ProbeRates
          target={target}
          preset={presets.data?.find((candidate) => presetReference(candidate) === preset)}
          onApply={(rates) => setEditedSpace(withProbeRates(space, rates))}
        />
      )}
      <Group align="end">
        <NumberInput
          label="Max runs"
          min={1}
          max={500}
          value={maxRuns}
          onChange={(value) => setMaxRuns(Number(value) || 1)}
          w={140}
        />
        <NumberInput
          label="Max hours"
          min={0.5}
          max={72}
          step={0.5}
          value={maxHours}
          onChange={(value) => setMaxHours(Number(value) || 1)}
          w={140}
        />
        <Stack gap={4}>
          <Text size="sm" fw={500}>
            Steps
          </Text>
          <SegmentedControl
            value={mode}
            onChange={(value) => setMode(value as "auto" | "manual")}
            data={[
              { value: "auto", label: "Run on their own" },
              { value: "manual", label: "Wait for my approval" },
            ]}
          />
        </Stack>
      </Group>
      {create.isError && (
        <Alert color="red" title="Not started">
          {create.error.message}
        </Alert>
      )}
      <Group>
        <Button
          onClick={submit}
          disabled={!targetId || !preset || spaceError !== null || hasKnobProblem}
          loading={create.isPending}
        >
          Start session
        </Button>
      </Group>
    </Stack>
  );
}

const templateOption = (template: SearchSpaceTemplate) => ({ value: templateKey(template), label: template.title });
