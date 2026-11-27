import { Alert, Anchor, Button, Grid, Group, Loader, Select, Stack, Text, TextInput, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { useSaveScenario, useScenario, useScenarioPlan, useScenarios } from "../../api/hooks";
import type { PlanCreate, PlanRequest } from "../../api/types";
import { CalculationTree, type Overrides } from "./CalculationTree";
import { PlanInputs } from "./PlanInputs";
import { PlanResults } from "./PlanResults";

// Inputs settle for this long before the plan is recomputed (debounced).
const RECOMPUTE_DEBOUNCE_MILLISECONDS = 400;

const DEFAULT_REQUEST: PlanRequest = {
  peak_rps: 100,
  slos: { ttft_p95: "500ms" },
  availability: { min_replicas: 2, spare: false, zones: 1 },
};

/**
 * The capacity planner and the scenario workbench are one page. Every number comes from servtuner plan through
 * POST /scenarios/evaluate; the page does no plan math.
 */
export function PlannerPage() {
  const { scenarioId } = useParams();
  const [searchParams] = useSearchParams();
  const saved = useScenario(scenarioId);
  if (scenarioId && saved.isPending) return <Loader size="sm" aria-label="Loading the scenario" />;
  if (scenarioId && saved.isError) {
    return (
      <Alert color="red" title="No such scenario">
        {saved.error.message}.{" "}
        <Anchor component={Link} to="/planner">
          Start a new plan
        </Anchor>
      </Alert>
    );
  }
  const fromRuns = (searchParams.get("runs") ?? "").split(",").filter(Boolean);
  const initial: PlanCreate = saved.data?.scenario ?? { request: DEFAULT_REQUEST, run_ids: fromRuns };
  return (
    <Workbench
      key={scenarioId ?? "new"}
      initial={initial}
      savedName={saved.data?.name}
      savedNote={saved.data?.note ?? undefined}
    />
  );
}

function splitOverrides(request: PlanRequest): [PlanRequest, Overrides] {
  const { overrides, ...rest } = request;
  return [rest, new Map((overrides ?? []).map((override) => [override.node, override]))];
}

function Workbench({ initial, savedName, savedNote }: { initial: PlanCreate; savedName?: string; savedNote?: string }) {
  const [initialRequest, initialOverrides] = splitOverrides(initial.request);
  const [request, setRequest] = useState<PlanRequest>(initialRequest);
  const [runIds, setRunIds] = useState<string[]>(initial.run_ids);
  const [overrides, setOverrides] = useState<Overrides>(initialOverrides);

  const scenario = useMemo<PlanCreate | null>(() => {
    if (runIds.length === 0 || !(request.peak_rps > 0) || Object.keys(request.slos ?? {}).length === 0) return null;
    const list = [...overrides.values()];
    return { request: list.length ? { ...request, overrides: list } : request, run_ids: runIds };
  }, [request, runIds, overrides]);
  const baselineBody = useMemo<PlanCreate | null>(
    () => (scenario && overrides.size > 0 ? { request, run_ids: runIds } : null),
    [scenario, overrides.size, request, runIds],
  );
  const [debounced] = useDebouncedValue(scenario, RECOMPUTE_DEBOUNCE_MILLISECONDS);
  const [debouncedBaseline] = useDebouncedValue(baselineBody, RECOMPUTE_DEBOUNCE_MILLISECONDS);
  const plan = useScenarioPlan(debounced);
  const baseline = useScenarioPlan(debouncedBaseline);

  return (
    <Stack gap="md">
      <Group justify="space-between" align="end">
        <Title order={2}>{savedName ?? "Capacity planner"}</Title>
        <OpenSaved />
      </Group>
      {savedNote && <Text c="dimmed">{savedNote}</Text>}
      <Grid gap="xl">
        <Grid.Col span={{ base: 12, md: 4 }}>
          <PlanInputs request={request} onRequest={setRequest} runIds={runIds} onRunIds={setRunIds} />
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 8 }}>
          <Stack gap="xl">
            {scenario === null ? (
              <Text c="dimmed">Choose at least one analysed run, a peak RPS above zero and one SLO to see a plan.</Text>
            ) : plan.isError ? (
              <Alert color="red" title="servtuner could not plan this">
                {plan.error.message}
              </Alert>
            ) : plan.data === undefined ? (
              <Loader size="sm" aria-label="Planning" />
            ) : (
              <>
                <PlanResults
                  plan={plan.data}
                  baseline={overrides.size > 0 ? baseline.data : undefined}
                  stale={plan.isFetching}
                />
                <CalculationTree nodes={plan.data.derivation.nodes} overrides={overrides} onOverrides={setOverrides} />
              </>
            )}
            {scenario && <SaveScenario scenario={scenario} defaultName={savedName} />}
          </Stack>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}

function OpenSaved() {
  const scenarios = useScenarios();
  const navigate = useNavigate();
  const data = (scenarios.data ?? []).map((scenario) => ({ value: scenario.id, label: scenario.name }));
  if (data.length === 0) return null;
  return (
    <Select
      aria-label="Open a saved scenario"
      placeholder="Open a saved scenario"
      data={data}
      searchable
      w={280}
      onChange={(id) => id && navigate(`/planner/${id}`)}
    />
  );
}

function SaveScenario({ scenario, defaultName }: { scenario: PlanCreate; defaultName?: string }) {
  const [name, setName] = useState(defaultName ?? "");
  const [note, setNote] = useState("");
  const save = useSaveScenario();
  const navigate = useNavigate();
  return (
    <Stack gap="xs">
      <Title order={4}>Save scenario</Title>
      <Group align="end">
        <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} w={240} />
        <TextInput label="Note" value={note} onChange={(e) => setNote(e.currentTarget.value)} style={{ flex: 1 }} />
        <Button
          disabled={!name.trim()}
          loading={save.isPending}
          onClick={() =>
            save.mutate(
              { name, note: note || undefined, scenario },
              { onSuccess: (saved) => navigate(`/planner/${saved.id}`) },
            )
          }
        >
          Save scenario
        </Button>
      </Group>
      {save.isError && (
        <Text size="sm" c="red" role="alert">
          {save.error.message}
        </Text>
      )}
    </Stack>
  );
}
