import type {
  AnalysisResult,
  AuditEvent,
  AuthStatus,
  AuthUser,
  IdentityLink,
  LoginRequest,
  PasswordChange,
  SessionView,
  SetupRequest,
  TemporaryPassword,
  UserAdmin,
  UserCreate,
  UserUpdate,
  BundleFile,
  CapacityTable,
  ClientTimeseries,
  CredentialInput,
  Environment,
  ExperimentAnalysis,
  ExperimentAnalyze,
  ExperimentDeleteImpact,
  ExperimentDetail,
  ExperimentResource,
  Followup,
  FollowupCreate,
  Host,
  HostCreate,
  HostCredentialSlot,
  PriceBook,
  PriceBookResource,
  InFlightChart,
  KnobCatalog,
  KnobServer,
  LatencyHistograms,
  LauncherCheck,
  ListRunEventsResponse,
  ListRunsQuery,
  ListRunsResponse,
  ManagedTarget,
  ManagedTargetCreate,
  ManagedTargetLog,
  ManagedTargetReadiness,
  Plan,
  PlanCreate,
  Recommendations,
  RunComparison,
  RunDefaults,
  RunDeleteImpact,
  RunDetail,
  RunResource,
  SavedTarget,
  SavedTargetCheck,
  Scenario,
  ScenarioCreate,
  Schedule,
  ScheduleCreate,
  ScheduleDetail,
  ScheduleUpdate,
  SearchSpaceTemplate,
  ServerChart,
  SpecValidation,
  TargetCheckView,
  TargetCreate,
  TargetSnapshot,
  TuningSession,
  TuningSessionCreate,
  UnsavedTargetCheck,
  VersionResource,
  VllmKnobs,
  WorkloadPreset,
} from "./types";

// A small hand-written wrapper instead of openapi-fetch: every type comes from schema.d.ts,
// and it keeps the dependency list short.

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly url: string,
    readonly detail?: string,
    method = "GET",
    /** An auth refusal's code: "setup_required", "forbidden", "cross_site", ... */
    readonly code?: string,
  ) {
    super(
      detail ? `${method} ${url} failed with HTTP ${status}: ${detail}` : `${method} ${url} failed with HTTP ${status}`,
    );
    this.name = "ApiError";
  }
}

interface ErrorBody {
  detail?: string;
  code?: string;
}

/** FastAPI's {"detail": …}: text to show, and an auth refusal's code and message. */
async function errorBody(response: Response): Promise<ErrorBody> {
  try {
    const body: unknown = await response.json();
    if (body && typeof body === "object" && "detail" in body) {
      const detail = (body as { detail: unknown }).detail;
      if (typeof detail === "string") return { detail };
      if (detail && typeof detail === "object" && "code" in detail && "message" in detail) {
        const refusal = detail as { code: unknown; message: unknown };
        return { detail: String(refusal.message), code: String(refusal.code) };
      }
      return { detail: JSON.stringify(detail) };
    }
  } catch {
    // Not JSON: the status alone says it.
  }
  return {};
}

async function apiError(response: Response, url: string, method: string): Promise<ApiError> {
  const { detail, code } = await errorBody(response);
  return new ApiError(response.status, url, detail, method, code);
}

export interface ApiClient {
  /** The running api build, for the header and the version check. */
  getVersion(): Promise<VersionResource>;
  /** Which screen the app shows: setup, login, or the app. */
  getAuthStatus(): Promise<AuthStatus>;
  login(body: LoginRequest): Promise<AuthUser>;
  logout(): Promise<void>;
  /** Creates the first admin; the token comes from the chart's setup token Secret. */
  setup(body: SetupRequest, setupToken: string): Promise<AuthUser>;
  changePassword(body: PasswordChange): Promise<void>;
  listMySessions(): Promise<SessionView[]>;
  revokeMySession(id: string): Promise<void>;
  revokeMyOtherSessions(): Promise<{ revoked: number }>;
  listUsers(): Promise<UserAdmin[]>;
  createUser(body: UserCreate): Promise<UserAdmin>;
  updateUser(id: string, body: UserUpdate): Promise<UserAdmin>;
  resetUserPassword(id: string): Promise<TemporaryPassword>;
  linkUserIdentity(id: string, body: IdentityLink): Promise<UserAdmin>;
  unlinkUserIdentity(id: string, subject: string): Promise<UserAdmin>;
  revokeUserSessions(id: string): Promise<void>;
  listAudit(beforeId?: number): Promise<AuditEvent[]>;
  listRuns(query: ListRunsQuery): Promise<ListRunsResponse>;
  getRun(runId: string): Promise<RunDetail>;
  listRunEvents(runId: string): Promise<ListRunEventsResponse>;
  getAnalysis(runId: string): Promise<AnalysisResult>;
  getRecommendations(runId: string): Promise<Recommendations>;
  getCapacity(runId: string): Promise<CapacityTable>;
  getClientTimeseries(runId: string): Promise<ClientTimeseries>;
  getHistograms(runId: string): Promise<LatencyHistograms>;
  getEnvironment(runId: string): Promise<Environment>;
  getInFlight(runId: string): Promise<InFlightChart>;
  getServerChart(runId: string): Promise<ServerChart>;
  getSnapshot(runId: string): Promise<TargetSnapshot>;
  listFiles(runId: string): Promise<BundleFile[]>;
  /** The URL that downloads one bundle file (the API may redirect to a presigned link). */
  fileUrl(runId: string, path: string): string;
  compareRuns(runIds: string[]): Promise<RunComparison>;
  createFollowup(runId: string, request: FollowupCreate): Promise<Followup>;
  confirmApplied(runId: string): Promise<RunResource>;
  validateSpec(text: string): Promise<SpecValidation>;
  /** The workload presets bench carries. */
  listWorkloadPresets(): Promise<WorkloadPreset[]>;
  evaluateScenario(body: PlanCreate): Promise<Plan>;
  listScenarios(): Promise<Scenario[]>;
  getScenario(id: string): Promise<Scenario>;
  saveScenario(body: ScenarioCreate): Promise<Scenario>;
  listSchedules(): Promise<Schedule[]>;
  getSchedule(id: string): Promise<ScheduleDetail>;
  createSchedule(body: ScheduleCreate): Promise<Schedule>;
  updateSchedule(id: string, body: ScheduleUpdate): Promise<Schedule>;
  triggerSchedule(id: string): Promise<RunResource>;
  deleteSchedule(id: string): Promise<void>;
  listTargets(): Promise<SavedTarget[]>;
  getTarget(id: string): Promise<SavedTarget>;
  createTarget(body: TargetCreate): Promise<SavedTarget>;
  updateTarget(id: string, body: TargetCreate): Promise<SavedTarget>;
  deleteTarget(id: string): Promise<void>;
  listManagedTargets(): Promise<ManagedTarget[]>;
  listHosts(): Promise<Host[]>;
  getHost(id: string): Promise<Host>;
  createHost(body: HostCreate): Promise<Host>;
  updateHost(id: string, body: HostCreate): Promise<Host>;
  deleteHost(id: string): Promise<void>;
  /** Read the machine through the launcher of a target on it, and keep it as the host's hardware. */
  readHostMachine(id: string): Promise<Host>;
  getPriceBook(): Promise<PriceBookResource>;
  /** Replace the install's price book; runs already made keep their prices. */
  savePriceBook(body: PriceBook): Promise<PriceBookResource>;
  /** Upload the token-price datasheet (LiteLLM's model-price JSON) as is. */
  uploadDatasheet(file: Blob): Promise<PriceBookResource>;
  deleteDatasheet(): Promise<void>;
  listSearchSpaces(): Promise<SearchSpaceTemplate[]>;
  getKnobCatalog(server?: KnobServer): Promise<KnobCatalog>;
  getRunDefaults(): Promise<RunDefaults>;
  createManagedTarget(body: ManagedTargetCreate): Promise<ManagedTarget>;
  getManagedTarget(id: string): Promise<ManagedTarget>;
  updateManagedTarget(id: string, body: ManagedTargetCreate): Promise<ManagedTarget>;
  /** Write-only, like a saved target's key: nothing comes back, and no call returns the value. */
  setManagedTargetCredential(id: string, slot: HostCredentialSlot, value: string): Promise<void>;
  deleteManagedTarget(id: string): Promise<void>;
  deployManagedTarget(id: string, knobs: VllmKnobs): Promise<ManagedTarget>;
  getManagedTargetReadiness(id: string): Promise<ManagedTargetReadiness>;
  getManagedTargetLogs(id: string, tailLines: number): Promise<ManagedTargetLog>;
  checkManagedTargetLauncher(id: string): Promise<LauncherCheck>;
  scaleDownManagedTarget(id: string): Promise<void>;
  listTuningSessions(): Promise<TuningSession[]>;
  getTuningSession(id: string): Promise<TuningSession>;
  createTuningSession(body: TuningSessionCreate): Promise<TuningSession>;
  approveTuningSession(id: string): Promise<TuningSession>;
  cancelTuningSession(id: string): Promise<TuningSession>;
  /** Write-only: nothing comes back, and no call ever returns the key. */
  setTargetCredential(id: string, slot: string, body: CredentialInput): Promise<void>;
  clearTargetCredential(id: string, slot: string): Promise<void>;
  checkTarget(id: string, body: SavedTargetCheck): Promise<TargetCheckView>;
  checkUnsavedTarget(body: UnsavedTargetCheck): Promise<TargetCheckView>;
  createRun(document: unknown): Promise<RunResource>;
  /** The RunSpec exactly as submitted (the bundle's spec.yaml), or null before bench wrote it. */
  getSubmittedSpec(runId: string): Promise<string | null>;
  listExperiments(): Promise<ExperimentResource[]>;
  getExperiment(name: string): Promise<ExperimentDetail>;
  getRunDeleteImpact(runId: string): Promise<RunDeleteImpact>;
  deleteRun(runId: string): Promise<void>;
  getExperimentDeleteImpact(name: string): Promise<ExperimentDeleteImpact>;
  deleteExperiment(name: string, deleteScenarios: boolean): Promise<void>;
  analyzeExperiment(name: string, request: ExperimentAnalyze): Promise<ExperimentAnalysis>;
}

export function createApiClient(baseUrl: string): ApiClient {
  async function get<Body>(path: string, query: Record<string, string | number | null | undefined> = {}) {
    const params = new URLSearchParams();
    for (const [name, value] of Object.entries(query)) {
      if (value !== undefined && value !== null && value !== "") params.set(name, String(value));
    }
    const queryString = params.size > 0 ? `?${params}` : "";
    const url = `${baseUrl}${path}${queryString}`;
    const response = await fetch(url, {
      headers: { Accept: "application/json" },
    });
    if (!response.ok) throw await apiError(response, url, "GET");
    return (await response.json()) as Body;
  }

  async function send(
    method: "POST" | "PUT" | "PATCH" | "DELETE",
    path: string,
    body?: unknown,
    headers: Record<string, string> = {},
  ) {
    const url = `${baseUrl}${path}`;
    const response = await fetch(url, {
      method,
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
        ...headers,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw await apiError(response, url, method);
    return response;
  }

  async function post<Body>(path: string, body: unknown) {
    return (await (await send("POST", path, body)).json()) as Body;
  }

  async function postText<Body>(path: string, text: string) {
    const url = `${baseUrl}${path}`;
    const response = await fetch(url, {
      method: "POST",
      headers: { Accept: "application/json", "Content-Type": "text/plain" },
      body: text,
    });
    if (!response.ok) throw await apiError(response, url, "POST");
    return (await response.json()) as Body;
  }

  const experiment = (name: string) => `/experiments/${encodeURIComponent(name)}`;
  const run = (runId: string) => `/runs/${encodeURIComponent(runId)}`;
  const schedule = (id: string) => `/schedules/${encodeURIComponent(id)}`;
  const target = (id: string) => `/targets/${encodeURIComponent(id)}`;
  const session = (id: string) => `/tuning-sessions/${encodeURIComponent(id)}`;
  const managedTarget = (id: string) => `/managed-targets/${encodeURIComponent(id)}`;
  const user = (id: string) => `/users/${encodeURIComponent(id)}`;
  return {
    getVersion: () => get<VersionResource>("/version"),
    getAuthStatus: () => get<AuthStatus>("/auth/status"),
    login: (body) => post<AuthUser>("/auth/login", body),
    logout: async () => {
      await send("POST", "/auth/logout");
    },
    setup: async (body, setupToken) =>
      (await (await send("POST", "/auth/setup", body, { "X-Servtuner-Setup-Token": setupToken })).json()) as AuthUser,
    changePassword: async (body) => {
      await send("POST", "/auth/password", body);
    },
    listMySessions: () => get<SessionView[]>("/auth/sessions"),
    revokeMySession: async (id) => {
      await send("DELETE", `/auth/sessions/${encodeURIComponent(id)}`);
    },
    revokeMyOtherSessions: () => post<{ revoked: number }>("/auth/sessions/revoke-others", {}),
    listUsers: () => get<UserAdmin[]>("/users"),
    createUser: (body) => post<UserAdmin>("/users", body),
    updateUser: async (id, body) => (await (await send("PATCH", user(id), body)).json()) as UserAdmin,
    resetUserPassword: (id) => post<TemporaryPassword>(`${user(id)}/password`, {}),
    linkUserIdentity: (id, body) => post<UserAdmin>(`${user(id)}/identities`, body),
    unlinkUserIdentity: async (id, subject) =>
      (await (
        await send("DELETE", `${user(id)}/identities?subject=${encodeURIComponent(subject)}`)
      ).json()) as UserAdmin,
    revokeUserSessions: async (id) => {
      await send("DELETE", `${user(id)}/sessions`);
    },
    listAudit: (beforeId) => get<AuditEvent[]>("/audit", { before_id: beforeId }),
    listRuns: (query) => get<ListRunsResponse>("/runs", query),
    getRun: (runId) => get<RunDetail>(`/runs/${encodeURIComponent(runId)}`),
    listRunEvents: (runId) => get<ListRunEventsResponse>(`${run(runId)}/events`),
    getAnalysis: (runId) => get<AnalysisResult>(`${run(runId)}/analysis`),
    getRecommendations: (runId) => get<Recommendations>(`${run(runId)}/recommendations`),
    getCapacity: (runId) => get<CapacityTable>(`${run(runId)}/capacity`),
    getClientTimeseries: (runId) => get<ClientTimeseries>(`${run(runId)}/charts/client_timeseries`),
    getHistograms: (runId) => get<LatencyHistograms>(`${run(runId)}/charts/histograms`),
    getServerChart: (runId) => get<ServerChart>(`${run(runId)}/charts/server`),
    getInFlight: (runId) => get<InFlightChart>(`${run(runId)}/charts/inflight`),
    getEnvironment: (runId) => get<Environment>(`${run(runId)}/environment`),
    getSnapshot: (runId) => get<TargetSnapshot>(`${run(runId)}/snapshot`, { phase: "pre" }),
    listFiles: (runId) => get<BundleFile[]>(`${run(runId)}/files`),
    createFollowup: (runId, request) => post<Followup>(`${run(runId)}/followup`, request),
    evaluateScenario: (body) => post<Plan>("/scenarios/evaluate", body),
    listScenarios: () => get<Scenario[]>("/scenarios"),
    getScenario: (id) => get<Scenario>(`/scenarios/${encodeURIComponent(id)}`),
    saveScenario: (body) => post<Scenario>("/scenarios", body),
    listSchedules: () => get<Schedule[]>("/schedules"),
    getSchedule: (id) => get<ScheduleDetail>(schedule(id)),
    createSchedule: (body) => post<Schedule>("/schedules", body),
    updateSchedule: async (id, body) => (await (await send("PATCH", schedule(id), body)).json()) as Schedule,
    triggerSchedule: (id) => post<RunResource>(`${schedule(id)}/trigger`, {}),
    deleteSchedule: async (id) => {
      await send("DELETE", schedule(id));
    },
    listTargets: () => get<SavedTarget[]>("/targets"),
    getTarget: (id) => get<SavedTarget>(target(id)),
    createTarget: (body) => post<SavedTarget>("/targets", body),
    listManagedTargets: () => get<ManagedTarget[]>("/managed-targets"),
    listHosts: () => get<Host[]>("/hosts"),
    getHost: (id) => get<Host>(`/hosts/${encodeURIComponent(id)}`),
    createHost: (body) => post<Host>("/hosts", body),
    updateHost: async (id, body) =>
      (await (await send("PUT", `/hosts/${encodeURIComponent(id)}`, body)).json()) as Host,
    deleteHost: async (id) => {
      await send("DELETE", `/hosts/${encodeURIComponent(id)}`);
    },
    readHostMachine: (id) => post<Host>(`/hosts/${encodeURIComponent(id)}/read-machine`, {}),
    listSearchSpaces: () => get<SearchSpaceTemplate[]>("/search-spaces"),
    getKnobCatalog: (server = "vllm") => get<KnobCatalog>(`/knobs?server=${server}`),
    getRunDefaults: () => get<RunDefaults>("/run-defaults"),
    getPriceBook: () => get<PriceBookResource>("/price-book"),
    savePriceBook: async (body) => (await (await send("PUT", "/price-book", body)).json()) as PriceBookResource,
    uploadDatasheet: async (file) => {
      const url = `${baseUrl}/price-book/datasheet`;
      const response = await fetch(url, {
        method: "PUT",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: file,
      });
      if (!response.ok) throw await apiError(response, url, "PUT");
      return (await response.json()) as PriceBookResource;
    },
    deleteDatasheet: async () => {
      await send("DELETE", "/price-book/datasheet");
    },
    createManagedTarget: (body) => post<ManagedTarget>("/managed-targets", body),
    getManagedTarget: (id) => get<ManagedTarget>(managedTarget(id)),
    setManagedTargetCredential: async (id, slot, value) => {
      await send("PUT", `${managedTarget(id)}/credentials/${slot}`, { value });
    },
    updateManagedTarget: async (id, body) =>
      (await (await send("PUT", managedTarget(id), body)).json()) as ManagedTarget,
    deleteManagedTarget: async (id) => {
      await send("DELETE", managedTarget(id));
    },
    deployManagedTarget: (id, knobs) => post<ManagedTarget>(`${managedTarget(id)}/deploy`, { knobs }),
    getManagedTargetReadiness: (id) => get<ManagedTargetReadiness>(`${managedTarget(id)}/readiness`),
    getManagedTargetLogs: (id, tailLines) =>
      get<ManagedTargetLog>(`${managedTarget(id)}/logs`, { tail_lines: tailLines }),
    checkManagedTargetLauncher: (id) => post<LauncherCheck>(`${managedTarget(id)}/check-launcher`, {}),
    scaleDownManagedTarget: async (id) => {
      await send("POST", `${managedTarget(id)}/scale-down`);
    },
    listTuningSessions: () => get<TuningSession[]>("/tuning-sessions"),
    getTuningSession: (id) => get<TuningSession>(session(id)),
    createTuningSession: (body) => post<TuningSession>("/tuning-sessions", body),
    approveTuningSession: (id) => post<TuningSession>(`${session(id)}/approve`, {}),
    cancelTuningSession: (id) => post<TuningSession>(`${session(id)}/cancel`, {}),
    updateTarget: async (id, body) => (await (await send("PUT", target(id), body)).json()) as SavedTarget,
    deleteTarget: async (id) => {
      await send("DELETE", target(id));
    },
    setTargetCredential: async (id, slot, body) => {
      await send("PUT", `${target(id)}/credentials/${encodeURIComponent(slot)}`, body);
    },
    clearTargetCredential: async (id, slot) => {
      await send("DELETE", `${target(id)}/credentials/${encodeURIComponent(slot)}`);
    },
    checkTarget: (id, body) => post<TargetCheckView>(`${target(id)}/check`, body),
    checkUnsavedTarget: (body) => post<TargetCheckView>("/targets/check", body),
    validateSpec: (text) => postText<SpecValidation>("/specs/validate", text),
    listWorkloadPresets: () => get<WorkloadPreset[]>("/workload-presets"),
    createRun: (document) => post<RunResource>("/runs", document),
    getSubmittedSpec: async (runId) => {
      const url = `${baseUrl}${run(runId)}/files/spec.yaml`;
      const response = await fetch(url);
      if (response.status === 404) return null;
      if (!response.ok) throw await apiError(response, url, "GET");
      return response.text();
    },
    confirmApplied: (runId) => post<RunResource>(`${run(runId)}/confirm-applied`, {}),
    compareRuns: (runIds) => get<RunComparison>("/compare", { runs: runIds.join(",") }),
    listExperiments: () => get<ExperimentResource[]>("/experiments"),
    getExperiment: (name) => get<ExperimentDetail>(experiment(name)),
    getRunDeleteImpact: (runId) => get<RunDeleteImpact>(`${run(runId)}/delete-impact`),
    deleteRun: async (runId) => {
      await send("DELETE", run(runId));
    },
    getExperimentDeleteImpact: (name) => get<ExperimentDeleteImpact>(`${experiment(name)}/delete-impact`),
    deleteExperiment: async (name, deleteScenarios) => {
      await send("DELETE", `${experiment(name)}${deleteScenarios ? "?delete_scenarios=true" : ""}`);
    },
    analyzeExperiment: (name, request) => post<ExperimentAnalysis>(`${experiment(name)}/analyze`, request),
    fileUrl: (runId, path) => `${baseUrl}${run(runId)}/files/${path.split("/").map(encodeURIComponent).join("/")}`,
  };
}
