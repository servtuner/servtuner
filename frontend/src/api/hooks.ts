import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useMemo } from "react";
import { ApiError, type ApiClient } from "./client";
import { pollIntervalFor } from "./runStatus";
import type {
  HostCredentialSlot,
  ManagedTargetCreate,
  HostCreate,
  PriceBook,
  KnobDescription,
  KnobServer,
  VllmKnobs,
  TuningSession,
  TuningSessionCreate,
  AnalysisResult,
  CapacityTable,
  ExperimentAnalyze,
  FollowupCreate,
  ClientTimeseries,
  Environment,
  InFlightChart,
  ServerChart,
  LatencyHistograms,
  ListRunsQuery,
  PlanCreate,
  ScenarioCreate,
  ScheduleCreate,
  ScheduleUpdate,
  CredentialInput,
  SavedTargetCheck,
  TargetCreate,
  UnsavedTargetCheck,
  Recommendations,
  RunStatus,
  TargetSnapshot,
} from "./types";

export const ApiClientContext = createContext<ApiClient | null>(null);

export function useApiClient(): ApiClient {
  const client = useContext(ApiClientContext);
  if (client === null) throw new Error("useApiClient called outside ApiClientContext.Provider");
  return client;
}

export function useRuns(query: ListRunsQuery, isEnabled = true) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["runs", query],
    queryFn: () => client.listRuns(query),
    enabled: isEnabled,
    refetchInterval: (state) => pollIntervalFor(state.state.data?.map((run) => run.status)),
  });
}

export function useRun(runId: string) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["run", runId],
    queryFn: () => client.getRun(runId),
    refetchInterval: (state) => pollIntervalFor(state.state.data && [state.state.data.status]),
  });
}

// The run's status is part of the key, so the events refetch once when the run
// turns terminal and the final events land.
export function useRunEvents(runId: string, runStatus: RunStatus | undefined) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["run", runId, "events", runStatus],
    queryFn: () => client.listRunEvents(runId),
    refetchInterval: pollIntervalFor(runStatus && [runStatus]),
  });
}

// An analysis output of a run. The API answers 404 until bench has analysed the
// run, which is a normal state, not an error: the data is then null.
interface RunDocuments {
  getAnalysis: AnalysisResult;
  getRecommendations: Recommendations;
  getCapacity: CapacityTable;
  getClientTimeseries: ClientTimeseries;
  getHistograms: LatencyHistograms;
  getEnvironment: Environment;
  getInFlight: InFlightChart;
  getServerChart: ServerChart;
  getSnapshot: TargetSnapshot;
}

export function useRunDocument<Name extends keyof RunDocuments>(
  name: Name,
  runId: string,
  runStatus: RunStatus | undefined,
) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["run", runId, name, runStatus],
    queryFn: async () => {
      try {
        return (await client[name](runId)) as RunDocuments[Name];
      } catch (error) {
        if (error instanceof ApiError && error.status === 404) return null;
        throw error;
      }
    },
  });
}

export function useRunFiles(runId: string, runStatus: RunStatus | undefined) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["run", runId, "files", runStatus],
    queryFn: () => client.listFiles(runId),
  });
}

/** The preset catalog is immutable for a bench binary, so it is fetched once. */
/** What this install fills into a new RunSpec (its Prometheus); fixed for the api's life. */
export function useRunDefaults() {
  const client = useApiClient();
  return useQuery({ queryKey: ["run-defaults"], queryFn: () => client.getRunDefaults(), staleTime: Infinity });
}

/** The starting search spaces bench carries: immutable, like the presets. */
export function useSearchSpaces() {
  const client = useApiClient();
  return useQuery({ queryKey: ["search-spaces"], queryFn: () => client.listSearchSpaces(), staleTime: Infinity });
}

/** Every flag of a model server (vLLM by default, or SGLang) as a knob, with the server's own
 * description and limits (GET /knobs?server=); it changes only with the pinned version, so it is
 * read once per server. */
export function useKnobCatalog(server: KnobServer = "vllm") {
  const client = useApiClient();
  return useQuery({ queryKey: ["knobs", server], queryFn: () => client.getKnobCatalog(server), staleTime: Infinity });
}

/** GET /knobs by knob name (empty until it loads), with the query for its error. */
export function useKnobsByName(server: KnobServer = "vllm") {
  const query = useKnobCatalog(server);
  const catalog = useMemo(
    () => new Map<string, KnobDescription>((query.data?.knobs ?? []).map((knob) => [knob.name, knob])),
    [query.data],
  );
  return { query, catalog };
}

export function useWorkloadPresets() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["workload-presets"],
    queryFn: () => client.listWorkloadPresets(),
    staleTime: Infinity,
  });
}

export function useExperiments() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["experiments"],
    queryFn: () => client.listExperiments(),
  });
}

export function useExperiment(name: string) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["experiment", name],
    queryFn: () => client.getExperiment(name),
  });
}

/**
 * A user's delete of one run: the impact is read while the dialog is open (never
 * cached, since a block can clear at any moment), and a delete drops every list that showed it.
 */
export function useRunDeletion(runId: string, isOpen: boolean) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return {
    impact: useQuery({
      queryKey: ["run-delete-impact", runId],
      queryFn: () => client.getRunDeleteImpact(runId),
      enabled: isOpen,
      gcTime: 0,
    }),
    remove: useMutation({
      mutationFn: () => client.deleteRun(runId),
      onSuccess: () => {
        queryClient.removeQueries({ queryKey: ["run", runId] });
        return Promise.all(
          ["runs", "experiments", "experiment", "compare"].map((key) =>
            queryClient.invalidateQueries({ queryKey: [key] }),
          ),
        );
      },
    }),
  };
}

/** A user's delete of an experiment with what belongs to it. */
export function useExperimentDeletion(name: string, isOpen: boolean) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return {
    impact: useQuery({
      queryKey: ["experiment-delete-impact", name],
      queryFn: () => client.getExperimentDeleteImpact(name),
      enabled: isOpen,
      gcTime: 0,
    }),
    remove: useMutation({
      mutationFn: (deleteScenarios: boolean) => client.deleteExperiment(name, deleteScenarios),
      onSuccess: () => {
        queryClient.removeQueries({ queryKey: ["experiment", name] });
        return Promise.all(
          ["runs", "experiments", "tuning-sessions", "scenarios"].map((key) =>
            queryClient.invalidateQueries({ queryKey: [key] }),
          ),
        );
      },
    }),
  };
}

/** Runs servtuner compare over the experiment; the experiment view refetches to show it. */
export function useAnalyzeExperiment(name: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: ExperimentAnalyze) => client.analyzeExperiment(name, request),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["experiment", name] }),
  });
}

export function useRunComparison(runIds: string[]) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["compare", runIds],
    queryFn: () => client.compareRuns(runIds),
    enabled: runIds.length > 0,
  });
}

export function useCreateFollowup(runId: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: FollowupCreate) => client.createFollowup(runId, request),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["runs"] }),
  });
}

/** Launches every given follow-up run: the user has applied the change. */
export function useConfirmApplied() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (runIds: string[]) => Promise.all(runIds.map((runId) => client.confirmApplied(runId))),
    onSuccess: () => queryClient.invalidateQueries(),
  });
}

export function useValidateSpec() {
  const client = useApiClient();
  return useMutation({
    mutationFn: (text: string) => client.validateSpec(text),
  });
}

export function useCreateRun() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (document: unknown) => client.createRun(document),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["runs"] }),
  });
}

export function useSubmittedSpec(runId: string | null) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["run", runId, "spec.yaml"],
    queryFn: () => client.getSubmittedSpec(runId ?? ""),
    enabled: runId !== null,
  });
}

/** POST /scenarios/evaluate; the previous plan stays on screen while the next computes. */
export function useScenarioPlan(body: PlanCreate | null) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["scenario-plan", body],
    queryFn: () => client.evaluateScenario(body!),
    enabled: body !== null,
    placeholderData: keepPreviousData,
    retry: false,
  });
}

export function useScenarios() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["scenarios"],
    queryFn: () => client.listScenarios(),
  });
}

export function useScenario(id: string | undefined) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["scenario", id],
    queryFn: () => client.getScenario(id!),
    enabled: id !== undefined,
  });
}

export function useSaveScenario() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ScenarioCreate) => client.saveScenario(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["scenarios"] }),
  });
}

export function useSchedules() {
  const client = useApiClient();
  return useQuery({
    queryKey: ["schedules"],
    queryFn: () => client.listSchedules(),
  });
}

/** A schedule and its judged runs; polled while a run of it is unfinished. */
export function useSchedule(id: string) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["schedule", id],
    queryFn: () => client.getSchedule(id),
    refetchInterval: (query) =>
      query.state.data?.runs.some((run) => run.verdict === "pending") ? SCHEDULE_POLL_MS : false,
  });
}

const SCHEDULE_POLL_MS = 5000;

export function useCreateSchedule() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: ScheduleCreate) => client.createSchedule(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["schedules"] }),
  });
}

/** PATCH, trigger and delete of one schedule; each refreshes it and the list. */
export function useScheduleActions(id: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["schedule", id] }),
      queryClient.invalidateQueries({ queryKey: ["schedules"] }),
    ]);
  return {
    update: useMutation({
      mutationFn: (body: ScheduleUpdate) => client.updateSchedule(id, body),
      onSuccess: refresh,
    }),
    trigger: useMutation({
      mutationFn: () => client.triggerSchedule(id),
      onSuccess: refresh,
    }),
    remove: useMutation({
      mutationFn: () => client.deleteSchedule(id),
      onSuccess: refresh,
    }),
  };
}

export function useTargets() {
  const client = useApiClient();
  return useQuery({ queryKey: ["targets"], queryFn: () => client.listTargets() });
}

export function useTarget(id: string) {
  const client = useApiClient();
  return useQuery({ queryKey: ["target", id], queryFn: () => client.getTarget(id) });
}

export function useCreateTarget() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: TargetCreate) => client.createTarget(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["targets"] }),
  });
}

/** Edit, delete, keys and checks of one saved target; each refreshes it and the list. */
export function useTargetActions(id: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["target", id] }),
      queryClient.invalidateQueries({ queryKey: ["targets"] }),
    ]);
  return {
    update: useMutation({
      mutationFn: (body: TargetCreate) => client.updateTarget(id, body),
      onSuccess: refresh,
    }),
    remove: useMutation({
      mutationFn: () => client.deleteTarget(id),
      onSuccess: () => queryClient.invalidateQueries({ queryKey: ["targets"] }),
    }),
    setCredential: useMutation({
      mutationFn: ({ slot, body }: { slot: string; body: CredentialInput }) =>
        client.setTargetCredential(id, slot, body),
      onSuccess: refresh,
    }),
    clearCredential: useMutation({
      mutationFn: (slot: string) => client.clearTargetCredential(id, slot),
      onSuccess: refresh,
    }),
    check: useMutation({
      mutationFn: (body: SavedTargetCheck) => client.checkTarget(id, body),
      onSuccess: refresh,
    }),
  };
}

/** Check an unsaved target block (New run); nothing is stored. */
export function useCheckUnsavedTarget() {
  const client = useApiClient();
  return useMutation({ mutationFn: (body: UnsavedTargetCheck) => client.checkUnsavedTarget(body) });
}

export function useHosts() {
  const client = useApiClient();
  return useQuery({ queryKey: ["hosts"], queryFn: () => client.listHosts() });
}

export function useHost(id: string) {
  const client = useApiClient();
  return useQuery({ queryKey: ["hosts", id], queryFn: () => client.getHost(id) });
}

/** Create a host, or replace one (id given). */
export function useSaveHost(id?: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: HostCreate) => (id ? client.updateHost(id, body) : client.createHost(body)),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["hosts"] }),
  });
}

export function usePriceBook() {
  const client = useApiClient();
  return useQuery({ queryKey: ["price-book"], queryFn: () => client.getPriceBook() });
}

/** Replace the price book; new runs' defaults and managed targets' run_analysis follow it. */
export function useSavePriceBook() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PriceBook) => client.savePriceBook(body),
    onSuccess: () => {
      for (const key of ["price-book", "run-defaults", "managed-targets", "managed-target"])
        queryClient.invalidateQueries({ queryKey: [key] });
    },
  });
}

/** Upload or remove the price book's datasheet; datasheet-priced targets and new runs follow it. */
export function useDatasheet() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () => {
    for (const key of ["price-book", "run-defaults", "targets", "target"])
      queryClient.invalidateQueries({ queryKey: [key] });
  };
  return {
    upload: useMutation({ mutationFn: (file: Blob) => client.uploadDatasheet(file), onSuccess: refresh }),
    remove: useMutation({ mutationFn: () => client.deleteDatasheet(), onSuccess: refresh }),
  };
}

export function useDeleteHost() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => client.deleteHost(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["hosts"] }),
  });
}

export function useReadHostMachine(id: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => client.readHostMachine(id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["hosts"] }),
  });
}

export function useManagedTargets() {
  const client = useApiClient();
  return useQuery({ queryKey: ["managed-targets"], queryFn: () => client.listManagedTargets() });
}

export type ManagedTargetSave = {
  body: ManagedTargetCreate;
  // Pasted host credentials, sent after the target is saved (write-only, never in the body).
  credentials: Partial<Record<HostCredentialSlot, string>>;
};

/** Create (no id) or edit a managed target, then send any pasted credentials; returns it fresh. */
export function useSaveManagedTarget(id?: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ body, credentials }: ManagedTargetSave) => {
      const saved = id ? await client.updateManagedTarget(id, body) : await client.createManagedTarget(body);
      const pasted = Object.entries(credentials) as [HostCredentialSlot, string][];
      for (const [slot, value] of pasted) await client.setManagedTargetCredential(saved.id, slot, value);
      return pasted.length > 0 ? client.getManagedTarget(saved.id) : saved;
    },
    onSuccess: (saved) =>
      Promise.all(
        [["managed-targets"], ["managed-target", saved.id]].map((queryKey) =>
          queryClient.invalidateQueries({ queryKey }),
        ),
      ),
  });
}

export function useManagedTarget(id: string) {
  const client = useApiClient();
  return useQuery({ queryKey: ["managed-target", id], queryFn: () => client.getManagedTarget(id) });
}

/** A vLLM that is still booting is polled until it is ready or its boot ended. */
export const BOOTING_POLL_MS = 5000;

/**
 * One readiness poll per refetch (a servtuner check-target once the pods are up), so it runs only
 * while the target has a deployment and stops as soon as the boot has an answer.
 */
export function useManagedTargetReadiness(id: string, isDeployed: boolean) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["managed-target-readiness", id],
    queryFn: () => client.getManagedTargetReadiness(id),
    enabled: isDeployed,
    refetchInterval: (query) => (query.state.data?.state === "pending" ? BOOTING_POLL_MS : false),
  });
}

/**
 * vLLM's log tail; it follows a boot (polled while the target is starting), else on demand. The
 * boot state is in the key, so the end of a boot fetches once more: the last lines (the server's
 * startup) come after the final poll.
 */
export function useManagedTargetLogs(id: string, tailLines: number, isBooting: boolean) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["managed-target-logs", id, tailLines, isBooting],
    queryFn: () => client.getManagedTargetLogs(id, tailLines),
    refetchInterval: isBooting ? BOOTING_POLL_MS : false,
  });
}

/** Deploy, scale down, check the launcher of and delete one managed target; each refreshes it, its readiness and the list. */
export function useManagedTargetActions(id: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () =>
    Promise.all(
      [["managed-target", id], ["managed-target-readiness", id], ["managed-target-logs", id], ["managed-targets"]].map(
        (queryKey) => queryClient.invalidateQueries({ queryKey }),
      ),
    );
  return {
    deploy: useMutation({
      mutationFn: (knobs: VllmKnobs) => client.deployManagedTarget(id, knobs),
      onSuccess: refresh,
    }),
    scaleDown: useMutation({ mutationFn: () => client.scaleDownManagedTarget(id), onSuccess: refresh }),
    // Read-only: reaches the host's launcher with its token; nothing to refresh.
    checkLauncher: useMutation({ mutationFn: () => client.checkManagedTargetLauncher(id) }),
    remove: useMutation({
      mutationFn: () => client.deleteManagedTarget(id),
      onSuccess: () => queryClient.invalidateQueries({ queryKey: ["managed-targets"] }),
    }),
  };
}

export function useTuningSessions() {
  const client = useApiClient();
  return useQuery({ queryKey: ["tuning-sessions"], queryFn: () => client.listTuningSessions() });
}

/** Sessions that are still active advance every few seconds, so their page polls. */
export const ACTIVE_SESSION_POLL_MS = 5000;

export function useTuningSession(id: string) {
  const client = useApiClient();
  return useQuery({
    queryKey: ["tuning-session", id],
    queryFn: () => client.getTuningSession(id),
    refetchInterval: (query) =>
      query.state.data && ["running", "awaiting_approval"].includes(query.state.data.status)
        ? ACTIVE_SESSION_POLL_MS
        : false,
  });
}

export function useCreateTuningSession() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: TuningSessionCreate) => client.createTuningSession(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["tuning-sessions"] }),
  });
}

/** Approve (manual mode) or cancel; each refreshes the session and the list. */
export function useTuningSessionActions(id: string) {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = (session: TuningSession) => {
    queryClient.setQueryData(["tuning-session", id], session);
    void queryClient.invalidateQueries({ queryKey: ["tuning-sessions"] });
  };
  return {
    approve: useMutation({ mutationFn: () => client.approveTuningSession(id), onSuccess: refresh }),
    cancel: useMutation({ mutationFn: () => client.cancelTuningSession(id), onSuccess: refresh }),
  };
}
