import type { components, paths } from "./schema";

// Every API type is derived from the generated schema. Nothing here is a copy.
type JsonResponse<Path extends keyof paths> = paths[Path] extends {
  get: { responses: { 200: { content: { "application/json": infer Body } } } };
}
  ? Body
  : never;

export type RunResource = components["schemas"]["RunResource"];
export type RunDetail = components["schemas"]["RunDetail"];
export type RunEvent = components["schemas"]["Event"];
export type StageSummary = components["schemas"]["StageSummary"];
export type RunStatus = components["schemas"]["RunStatus"];
export type TargetType = components["schemas"]["TargetType"];

export type ListRunsResponse = JsonResponse<"/runs">;
export type ListRunEventsResponse = JsonResponse<"/runs/{run_id}/events">;

export type ListRunsQuery = NonNullable<paths["/runs"]["get"]["parameters"]["query"]>;

// Analysis outputs of one run.
export type AnalysisResult = components["schemas"]["AnalysisResult"];
export type Recommendations = components["schemas"]["Recommendations"];
export type CapacityTable = components["schemas"]["CapacityTable"];
export type ClientTimeseries = components["schemas"]["ClientTimeseries"];
export type LatencyHistograms = components["schemas"]["LatencyHistograms"];
export type BootstrapInterval = components["schemas"]["BootstrapInterval"];
export type BundleFile = components["schemas"]["BundleFileResource"];

// Experiments.
export type ExperimentResource = components["schemas"]["ExperimentResource"];
export type WorkloadPreset = components["schemas"]["WorkloadPreset"];
export type ExperimentDetail = components["schemas"]["ExperimentDetail"];
export type ExperimentAnalysis = components["schemas"]["ExperimentAnalysisResource"];
export type ExperimentAnalyze = components["schemas"]["ExperimentAnalyze"];
export type Comparison = components["schemas"]["Comparison"];
export type ObjectiveName = components["schemas"]["ObjectiveName"];

// GET /compare: runs aligned by knob and stage (the api aligns; it never ranks).
export type RunComparison = components["schemas"]["ComparisonResource"];

// The follow-up flow.
export type FollowupCreate = components["schemas"]["FollowupCreate"];
export type Followup = components["schemas"]["FollowupResource"];

// What ran the benchmark, and the target as captured (the Config tab).
export type Environment = components["schemas"]["Environment"];
export type TargetSnapshot = components["schemas"]["TargetSnapshot"];
export type InFlightChart = components["schemas"]["InFlightChart"];
export type ServerChart = components["schemas"]["ServerChart"];
export type SpecValidation = components["schemas"]["SpecValidationResource"];
export type Plan = components["schemas"]["Plan"];
type GeneratedPlanCreate = components["schemas"]["PlanCreate"];
type GeneratedPlanRequest = GeneratedPlanCreate["request"];
// The generated models are response-side: a field with a default is required
// there. A request may leave such fields out, so these are request-side views
// of the same models (still checked against the schema by the api).
type Optional<T> = { [K in keyof T]?: T[K] };
export type PlanRequest = Optional<Omit<GeneratedPlanRequest, "peak_rps" | "slos" | "availability">> & {
  peak_rps: number;
  slos: Optional<GeneratedPlanRequest["slos"]>;
  availability?: Optional<NonNullable<GeneratedPlanRequest["availability"]>> | null;
};
export type PlanCreate = {
  request: PlanRequest;
  run_ids: string[];
  payload_run_ids?: string[];
};
export type Scenario = components["schemas"]["ScenarioResource"];
export type ScenarioCreate = {
  name: string;
  note?: string;
  scenario: PlanCreate;
};

// Scheduled regression runs (api/schedules.py).
export type Schedule = components["schemas"]["ScheduleResource"];
export type ScheduleDetail = components["schemas"]["ScheduleDetail"];
export type ScheduledRun = components["schemas"]["ScheduledRunResource"];
export type Verdict = components["schemas"]["Verdict"];
export type ScheduleCreate = Optional<components["schemas"]["ScheduleCreate"]> & {
  name: string;
  spec: Record<string, unknown>;
  every_hours: number;
};
export type ScheduleUpdate = Optional<components["schemas"]["ScheduleUpdate"]>;

// Saved targets. Keys are write-only: CredentialInput goes
// out, CredentialStatus (never a value) comes back.
export type SavedTarget = components["schemas"]["TargetResource"];
export type RunTarget = components["schemas"]["Target"];
export type CredentialStatus = components["schemas"]["CredentialStatus"];
export type TargetCheckView = components["schemas"]["TargetCheckView"];
export type CheckTargetResult = components["schemas"]["CheckTargetResult"];
export type TargetCreate = {
  name: string;
  description?: string;
  target: Record<string, unknown>;
  /** How runs against it are priced; null clears it. */
  pricing?: components["schemas"]["TargetCreate_TargetPricing"] | null;
};
// Request-side view: the generated model marks fields with defaults as required.
export type CredentialInput = {
  value?: string;
  username?: string;
  password?: string;
  external?: { name: string; key?: string; token_key?: string; username_key?: string; password_key?: string };
};
export type SavedTargetCheck = { generate?: boolean; api_key?: string };

// Managed targets and tuning sessions. Request-side
// views, since the generated models mark fields with defaults as required.
export type ManagedTarget = components["schemas"]["ManagedTargetResource"];
/** A machine servtuner runs vLLM on, its hardware and price. */
export type Host = components["schemas"]["HostResource"];
export type HostCreate = components["schemas"]["HostCreate"];
export type HostPrice = NonNullable<HostCreate["price"]>;
export type ManagedTargetSpec = components["schemas"]["ManagedTargetSpec"];
export type ManagedTargetReadiness = components["schemas"]["ReadinessView"];
export type ManagedTargetLog = components["schemas"]["LogView"];
export type LauncherCheck = components["schemas"]["LauncherCheckView"];
export type SearchSpaceTemplate = components["schemas"]["SearchSpaceTemplate"];
export type KnobCatalog = components["schemas"]["KnobCatalog"];
export type KnobDescription = components["schemas"]["KnobDescription"];
export type RunDefaults = components["schemas"]["RunDefaults"];
type GeneratedPriceBook = components["schemas"]["PriceBook"];
/** The PUT /price-book body, as the api accepts it: the generated model marks fields with defaults
 * as required. GET /price-book answers the same fields (PriceBookResource) with more. */
export type PriceBook = Optional<Omit<GeneratedPriceBook, "source" | "nodes">> & {
  source: GeneratedPriceBook["source"];
  nodes?: Record<string, Optional<NonNullable<GeneratedPriceBook["nodes"]>[string]> & NodeTypePriceRequired>;
};
type NodeTypePriceRequired = { usd_per_hour: number; cores: number; memory_gib: number };
export type PriceBookResource = components["schemas"]["PriceBookResource"];
export type RunDeleteImpact = components["schemas"]["DeleteImpactResource"];
export type ExperimentDeleteImpact = components["schemas"]["ExperimentDeleteImpactResource"];
/** A host target's write-only credentials (api managed_targets.HOST_CREDENTIAL_FIELDS). */
export type HostCredentialSlot = "launcher_token" | "api_key";
/** vLLM knobs by name (bench's knob table), as POST /managed-targets/{id}/deploy takes them. */
export type VllmKnobs = Record<string, string | number | boolean>;
export type ManagedTargetCreate = {
  name: string;
  description?: string;
  namespace: string;
  spec: Record<string, unknown>;
};
export type TuningSession = components["schemas"]["SessionResource"];
export type TuningSessionCreate = {
  managed_target_id: string;
  preset: string;
  streaming?: boolean;
  slos: Record<string, string | number>;
  space: Record<string, unknown>;
  budget: { max_runs: number; max_hours: number };
  mode?: "auto" | "manual";
  experiment?: string;
  reuse_from?: string[];
};
export type UnsavedTargetCheck = {
  target: Record<string, unknown>;
  prometheus?: Record<string, unknown>;
  generate?: boolean;
};

// User auth.
export type AuthStatus = components["schemas"]["AuthStatus"];
export type AuthUser = components["schemas"]["UserView"];
export type Action = components["schemas"]["Action"];
export type Role = components["schemas"]["Role"];
export type LoginRequest = components["schemas"]["LoginRequest"];
export type SetupRequest = components["schemas"]["SetupRequest"];
export type PasswordChange = components["schemas"]["PasswordChange"];
export type SessionView = components["schemas"]["SessionView"];
export type UserAdmin = components["schemas"]["UserAdminView"];
export type UserCreate = components["schemas"]["UserCreate"];
export type UserUpdate = components["schemas"]["UserUpdate"];
export type IdentityLink = components["schemas"]["IdentityLink"];
export type TemporaryPassword = components["schemas"]["TemporaryPassword"];
export type AuditEvent = components["schemas"]["AuditEventView"];
