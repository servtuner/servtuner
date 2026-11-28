import { generateName } from "./names";
import { parseDocument, stringify } from "yaml";
import type { HostCredentialSlot, ManagedTarget, ManagedTargetCreate } from "../../api/types";

export type Placement = "kubernetes" | "host";
export type CertificateSource = "plain" | "issuer" | "secret";

/** Every text field of the managed target form, for both placements. */
export type FormFields = {
  name: string;
  description: string;
  namespace: string;
  model: string;
  servedModelName: string;
  baseFlags: string;
  // NAME=value per line: vLLM's environment (never a secret: the token comes from a Secret).
  env: string;
  // Kubernetes
  image: string;
  cpu: string;
  memory: string;
  cache: string;
  // Where the weights come from: Hugging Face, an internal mirror, a seeded claim, or an
  // object store vLLM streams them from.
  modelSource: "hub" | "mirror" | "pvc" | "object_store";
  mirrorEndpoint: string;
  modelClaim: string;
  objectUri: string;
  objectEndpoint: string;
  objectSecret: string;
  // /dev/shm's size limit, a Kubernetes quantity; empty leaves it unbounded.
  shmSize: string;
  hfSecret: string;
  // key=value per line, and Kubernetes' tolerations as YAML: where the pod may run.
  nodeSelector: string;
  tolerations: string;
  issuerName: string;
  tlsSecret: string;
  // Host (servtuner launcher). The pasted token and key are sent write-only, never in the body.
  // The host record it runs on, by id; empty names none.
  hostId: string;
  launcherUrl: string;
  launcherToken: string;
  apiKey: string;
  endpoint: string;
  // Advanced: a Secret the user manages, instead of pasting; and how Prometheus labels the host.
  launcherTokenSecret: string;
  apiKeySecret: string;
  metricsJob: string;
  metricsPod: string;
  metricsNode: string;
};

/** The model server a Kubernetes target runs; a host target runs vLLM. */
export type ModelServer = "vllm" | "sglang";

export type FormState = {
  fields: FormFields;
  placement: Placement;
  server: ModelServer;
  gpus: number;
  transport: CertificateSource;
};

// The vLLM version the knob table is checked against, as its GPU
// image and its CPU build (for a target with no GPU, such as kind; deploy/kind/README.md).
export const VLLM_GPU_IMAGE = "vllm/vllm-openai:v0.34.0";
export const VLLM_CPU_IMAGE = "vllm/vllm-openai-cpu:v0.34.0-x86_64";
// The SGLang version its knob catalog is read from, as its serving (runtime) image (deploy/release/images.lock).
export const SGLANG_IMAGE = "lmsysorg/sglang:v0.5.23-runtime";

/** The state with another server: a blank or default image follows it to that server's image. */
export function withServer(state: FormState, server: ModelServer, registry?: string | null): FormState {
  const defaults = [VLLM_GPU_IMAGE, VLLM_CPU_IMAGE, SGLANG_IMAGE].flatMap((image) => [
    image,
    withRegistry(image, registry),
  ]);
  const image = state.fields.image.trim();
  const followsServer = image === "" || defaults.includes(image);
  const next = server === "sglang" ? SGLANG_IMAGE : VLLM_GPU_IMAGE;
  return {
    ...state,
    server,
    fields: followsServer ? { ...state.fields, image: withRegistry(next, registry) } : state.fields,
  };
}

/** An image under the install's registry (GET /run-defaults image_registry), named the way the
 * chart and `hauler store copy` name it: the registry replaces Docker Hub and the repository path is kept. */
export function withRegistry(image: string, registry: string | null | undefined): string {
  return registry ? `${registry.replace(/\/+$/, "")}/${image}` : image;
}

/** A vLLM with no GPU still set to the GPU image: it needs the CPU build to start. */
export function needsCpuImage(state: FormState): boolean {
  return (
    state.placement === "kubernetes" && state.gpus === 0 && /(^|\/)vllm\/vllm-openai:/.test(state.fields.image.trim())
  );
}

export const DEFAULT_GPUS = 1;
export const MAX_GPUS = 8; // one node: the api's limit
// The Secret key the form reads every token from (launcher token, vLLM key, Hugging Face token).
const TOKEN_KEY = "token";
// The scrape job the chart's servtuner-external jobs use for hosts outside the cluster, and the
// namespace label they give the series (a host has no namespace of its own).
const HOST_METRICS_JOB = "servtuner-external";
export const HOST_METRICS_NAMESPACE = "servtuner-external";

const BLANK: FormFields = {
  name: "",
  description: "",
  namespace: "",
  model: "",
  servedModelName: "",
  baseFlags: "",
  env: "",
  image: VLLM_GPU_IMAGE,
  cpu: "8",
  memory: "64Gi",
  cache: "200Gi",
  modelSource: "hub",
  mirrorEndpoint: "",
  modelClaim: "",
  objectUri: "",
  objectEndpoint: "",
  objectSecret: "",
  shmSize: "",
  hfSecret: "",
  nodeSelector: "",
  tolerations: "",
  issuerName: "",
  tlsSecret: "",
  hostId: "",
  launcherUrl: "",
  launcherToken: "",
  apiKey: "",
  endpoint: "",
  launcherTokenSecret: "",
  apiKeySecret: "",
  metricsJob: HOST_METRICS_JOB,
  metricsPod: "",
  metricsNode: "",
};

/** The form's starting state: blank for a new target (in a placement, its vLLM image under the install's
 * registry), its own values for an edit. */
export function initialState(
  existing?: ManagedTarget,
  placement: Placement = "kubernetes",
  registry?: string | null,
): FormState {
  if (!existing)
    return {
      fields: { ...BLANK, name: generateName(), image: withRegistry(VLLM_GPU_IMAGE, registry) },
      placement,
      server: "vllm",
      gpus: DEFAULT_GPUS,
      transport: "plain",
    };
  const common = {
    ...BLANK,
    name: existing.name,
    description: existing.description ?? "",
    namespace: existing.namespace,
    model: existing.spec.model,
    servedModelName: existing.spec.served_model_name ?? "",
    baseFlags: (existing.spec.base_flags ?? []).join("\n"),
    env: pairLines(existing.spec.env),
  };
  const spec = existing.spec;
  if (spec.placement === "host")
    return {
      placement: "host",
      server: "vllm",
      gpus: DEFAULT_GPUS,
      transport: "plain",
      fields: {
        ...common,
        hostId: spec.host_id ?? "",
        launcherUrl: spec.launcher_url,
        endpoint: spec.endpoint,
        // Only a Secret the user named shows; servtuner's own (a pasted value) stays hidden.
        launcherTokenSecret: externalSecret(existing, "launcher_token"),
        apiKeySecret: externalSecret(existing, "api_key"),
        metricsJob: spec.metrics.job,
        metricsPod: spec.metrics.pod,
        metricsNode: spec.metrics.node ?? "",
      },
    };
  // TLS, when the target serves it (client certificates are not offered yet).
  const tls = spec.transport?.enabled ? spec.transport : undefined;
  return {
    placement: "kubernetes",
    server: spec.server ?? "vllm",
    gpus: spec.gpus,
    transport: tls ? (tls.issuer ? "issuer" : "secret") : "plain",
    fields: {
      ...common,
      image: spec.image,
      cpu: spec.cpu,
      memory: spec.memory,
      cache: spec.model_cache?.size ?? BLANK.cache,
      modelSource: spec.model_source?.kind ?? "hub",
      mirrorEndpoint: spec.model_source?.kind === "mirror" ? spec.model_source.endpoint : "",
      modelClaim: spec.model_source?.kind === "pvc" ? spec.model_source.claim : "",
      objectUri: spec.model_source?.kind === "object_store" ? spec.model_source.uri : "",
      objectEndpoint: spec.model_source?.kind === "object_store" ? (spec.model_source.endpoint ?? "") : "",
      objectSecret: spec.model_source?.kind === "object_store" ? (spec.model_source.credentials?.name ?? "") : "",
      shmSize: spec.shm_size ?? "",
      hfSecret: spec.hf_token?.name ?? "",
      nodeSelector: pairLines(spec.node_selector),
      tolerations: spec.tolerations?.length ? stringify(spec.tolerations) : "",
      issuerName: tls?.issuer?.name ?? "",
      tlsSecret: tls?.secret ?? "",
    },
  };
}

const trimmed = (value: string) => value.trim();
type SecretRef = { name: string; key: string };

/** A Secret by name; an edit keeps the stored key (the form only names the Secret). */
function secretRef(name: string, kept: unknown): SecretRef {
  return { name: name.trim(), key: (kept as SecretRef | undefined)?.key ?? TOKEN_KEY };
}

const optionalSecretRef = (name: string, kept: unknown) => (name.trim() ? secretRef(name, kept) : undefined);

/**
 * The POST/PUT body. An edit keeps what the form does not show (node selector, tolerations,
 * env, port, served model name, a storage class): the spec starts from the stored one when the
 * placement is unchanged, and each shown field replaces its value.
 */
export function buildBody(state: FormState, existing?: ManagedTarget): ManagedTargetCreate {
  const { fields } = state;
  const kept: Record<string, unknown> =
    existing && existing.spec.placement === state.placement ? { ...existing.spec } : {};
  const baseFlags = fields.baseFlags.split("\n").map(trimmed).filter(Boolean);
  const env = pairsOrEmpty(fields.env);
  const spec = state.placement === "host" ? hostSpec(fields, kept) : kubernetesSpec(state, kept);
  return {
    name: trimmed(fields.name),
    ...(fields.description.trim() ? { description: trimmed(fields.description) } : {}),
    namespace: trimmed(fields.namespace) || (state.placement === "host" ? HOST_METRICS_NAMESPACE : ""),
    spec: dropUndefined({
      ...kept,
      ...spec,
      model: trimmed(fields.model),
      served_model_name: fields.servedModelName.trim() || undefined,
      base_flags: baseFlags,
      // An edit sends {} to clear stored env; a new target leaves it out (the api's default).
      env: clearableOnEdit(env, kept.env),
    }),
  };
}

function kubernetesSpec(state: FormState, kept: Record<string, unknown>): Record<string, unknown> {
  const { fields } = state;
  const keptCache = (kept.model_cache ?? {}) as Record<string, unknown>;
  // An edit keeps the issuer's kind (Issuer or ClusterIssuer) and group; the form names it.
  const keptIssuer = ((kept.transport as { issuer?: object } | undefined)?.issuer ?? {}) as Record<string, unknown>;
  return {
    placement: "kubernetes",
    server: state.server,
    image: trimmed(fields.image),
    gpus: state.gpus,
    cpu: trimmed(fields.cpu),
    memory: trimmed(fields.memory),
    // Only a download keeps a cache (null clears a stored one): a seeded claim or a stream has none.
    model_cache:
      fields.modelSource === "hub" || fields.modelSource === "mirror"
        ? { ...keptCache, size: trimmed(fields.cache) }
        : null,
    model_source:
      fields.modelSource === "mirror"
        ? { kind: "mirror", endpoint: trimmed(fields.mirrorEndpoint) }
        : fields.modelSource === "pvc"
          ? { kind: "pvc", claim: trimmed(fields.modelClaim) }
          : fields.modelSource === "object_store"
            ? dropUndefined({
                kind: "object_store",
                uri: trimmed(fields.objectUri),
                endpoint: fields.objectEndpoint.trim() || undefined,
                credentials: fields.objectSecret.trim() ? { name: trimmed(fields.objectSecret) } : undefined,
              })
            : // Hub is the api's default: sent only when an edit replaces a stored mirror or claim.
              kept.model_source
              ? { kind: "hub" }
              : undefined,
    // Cleared on an edit: null, so the stored limit goes (an absent field would keep it).
    shm_size: fields.shmSize.trim() || (kept.shm_size ? null : undefined),
    hf_token: optionalSecretRef(fields.hfSecret, kept.hf_token),
    node_selector: clearableOnEdit(pairsOrEmpty(fields.nodeSelector), kept.node_selector),
    tolerations: tolerationsOrNone(fields.tolerations, kept.tolerations),
    transport:
      state.transport === "issuer"
        ? { enabled: true, issuer: { kind: "ClusterIssuer", ...keptIssuer, name: trimmed(fields.issuerName) } }
        : state.transport === "secret"
          ? { enabled: true, secret: trimmed(fields.tlsSecret) }
          : { enabled: false },
  };
}

function hostSpec(fields: FormFields, kept: Record<string, unknown>): Record<string, unknown> {
  return {
    placement: "host",
    // Cleared on an edit: null, so the stored host goes (an absent field would keep it).
    host_id: fields.hostId || (kept.host_id ? null : undefined),
    launcher_url: trimmed(fields.launcherUrl),
    // A named Secret replaces the stored reference; otherwise it stays (a pasted value is sent
    // through the credentials route after the save).
    launcher_token: optionalSecretRef(fields.launcherTokenSecret, kept.launcher_token) ?? kept.launcher_token,
    endpoint: trimmed(fields.endpoint),
    auth: optionalSecretRef(fields.apiKeySecret, kept.auth) ?? kept.auth,
    metrics: dropUndefined({
      job: trimmed(fields.metricsJob),
      // The scrape config's pod label for this host; the target's name unless said otherwise.
      pod: trimmed(fields.metricsPod) || trimmed(fields.name),
      node: fields.metricsNode.trim() || undefined,
    }),
  };
}

// vLLM's own default port (vllm serve --port): where the launcher starts it unless told otherwise.
const VLLM_DEFAULT_PORT = 8000;

/** The endpoint runs reach when nothing (a port proxy) sits between: the launcher's host, vLLM's port. */
export function suggestedEndpoint(launcherUrl: string): string {
  try {
    const url = new URL(launcherUrl.trim());
    return url.hostname ? `http://${url.hostname}:${VLLM_DEFAULT_PORT}/v1` : "";
  } catch {
    return "";
  }
}

/** A new launcher URL also moves the endpoint, unless the user typed their own. */
export function withLauncherUrl(fields: FormFields, launcherUrl: string): FormFields {
  const isSuggested = !fields.endpoint || fields.endpoint === suggestedEndpoint(fields.launcherUrl);
  return { ...fields, launcherUrl, endpoint: isSuggested ? suggestedEndpoint(launcherUrl) : fields.endpoint };
}

/** The host credentials the user pasted, for the write-only route. */
export function pastedCredentials(state: FormState): Partial<Record<HostCredentialSlot, string>> {
  if (state.placement !== "host") return {};
  const pasted: Partial<Record<HostCredentialSlot, string>> = {};
  if (state.fields.launcherToken.trim()) pasted.launcher_token = state.fields.launcherToken.trim();
  if (state.fields.apiKey.trim()) pasted.api_key = state.fields.apiKey.trim();
  return pasted;
}

function externalSecret(target: ManagedTarget, slot: HostCredentialSlot): string {
  const status = target.credentials?.[slot];
  return status?.source === "external" ? (status.secret_name ?? "") : "";
}

const pairLines = (pairs: Record<string, string> | undefined) =>
  Object.entries(pairs ?? {})
    .map(([name, value]) => `${name}=${value}`)
    .join("\n");

function pairsOrEmpty(text: string): Record<string, string> {
  const parsed = parseKeyValues(text);
  return "pairs" in parsed ? parsed.pairs : {};
}

/** A map left empty: {} on an edit, to clear the stored one; left out on a new target. */
function clearableOnEdit(pairs: Record<string, string>, kept: unknown): Record<string, string> | undefined {
  return Object.keys(pairs).length > 0 ? pairs : kept ? {} : undefined;
}

function tolerationsOrNone(text: string, kept: unknown): Record<string, unknown>[] | undefined {
  const parsed = parseTolerations(text);
  const tolerations = "tolerations" in parsed ? parsed.tolerations : [];
  return tolerations.length > 0 ? tolerations : kept ? [] : undefined;
}

/** A cleared optional field is left out, so the api sees it unset rather than empty. */
function dropUndefined(object: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(object).filter(([, value]) => value !== undefined));
}

export type ParsedPairs = { pairs: Record<string, string> } | { error: string };

/** NAME=value per line (env, a node selector); the api checks the names. */
export function parseKeyValues(text: string): ParsedPairs {
  const pairs: Record<string, string> = {};
  for (const line of text.split("\n").map(trimmed).filter(Boolean)) {
    const equals = line.indexOf("=");
    if (equals <= 0) return { error: `Write NAME=value: ${line}` };
    pairs[line.slice(0, equals).trim()] = line.slice(equals + 1).trim();
  }
  return { pairs };
}

export type ParsedTolerations = { tolerations: Record<string, unknown>[] } | { error: string };

/** Pod tolerations as Kubernetes spells them: a YAML list of objects (the api checks the fields). */
export function parseTolerations(text: string): ParsedTolerations {
  if (!text.trim()) return { tolerations: [] };
  const document = parseDocument(text);
  const [firstError] = document.errors;
  if (firstError) return { error: firstError.message };
  const value: unknown = document.toJS();
  if (!Array.isArray(value) || !value.every((item) => item && typeof item === "object" && !Array.isArray(item)))
    return { error: "Write a YAML list, e.g. - {key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}" };
  return { tolerations: value as Record<string, unknown>[] };
}

/** The first problem in the form's free-text fields, which keeps Save disabled. */
export function formError(state: FormState): string | undefined {
  const checks: (ParsedPairs | ParsedTolerations)[] = [parseKeyValues(state.fields.env)];
  if (state.placement === "kubernetes") {
    const { modelSource, mirrorEndpoint, modelClaim } = state.fields;
    if (modelSource === "mirror" && !/^https?:\/\/\S+$/.test(mirrorEndpoint.trim()))
      return "The mirror's URL starts with http:// or https://";
    if (modelSource === "pvc" && !modelClaim.trim()) return "Name the claim seeded with the model";
    if (modelSource === "object_store" && state.server === "sglang")
      return "SGLang cannot stream weights from an object store: use Hugging Face, a mirror or a seeded claim";
    if (modelSource === "object_store" && !/^(s3|gs|az):\/\/[^/\s]+/.test(state.fields.objectUri.trim()))
      return "The weights' location starts with s3://, gs:// or az://";
    checks.push(parseKeyValues(state.fields.nodeSelector), parseTolerations(state.fields.tolerations));
  }
  return checks.map((check) => ("error" in check ? check.error : undefined)).find(Boolean);
}
