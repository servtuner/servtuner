import { describe, expect, it } from "vitest";
import type { ManagedTarget } from "../../api/types";
import { managedTarget } from "../../test/fixtures";
import { buildBody, formError, initialState, parseKeyValues, parseTolerations, withServer } from "./form";

const hostTarget: ManagedTarget = {
  ...managedTarget,
  name: "rtx",
  namespace: "vllm-lan",
  spec: {
    placement: "host",
    launcher_url: "http://gpu-host.localtest.me:9123",
    launcher_token: { name: "launcher-token", key: "launcher" },
    endpoint: "http://gpu-host.localtest.me:8000/v1",
    port: 8000,
    model: "Qwen/Qwen2.5-0.5B-Instruct",
    served_model_name: "tiny",
    base_flags: ["--max-model-len=4096"],
    env: {},
    auth: { name: "vllm-key", key: "token" },
    metrics: { job: "servtuner-external", pod: "vllm-lan", node: "vllm-lan-gpu" },
  },
};

describe("managed target form body", () => {
  it("reads the weights from a mirror or a seeded claim", () => {
    const state = initialState(undefined, "kubernetes", "registry.example.internal:5000");
    expect(state.fields.image).toBe("registry.example.internal:5000/vllm/vllm-openai:v0.34.0");
    state.fields = { ...state.fields, name: "q", namespace: "n", model: "m", modelSource: "pvc" };
    expect(formError(state)).toMatch(/claim/);
    state.fields.modelClaim = "weights";
    const seeded = buildBody(state).spec as Record<string, unknown>;
    expect([seeded.model_cache, seeded.model_source]).toEqual([null, { kind: "pvc", claim: "weights" }]);
    state.fields = { ...state.fields, modelSource: "mirror", mirrorEndpoint: "hf.example" };
    expect(formError(state)).toMatch(/http/);
    state.fields.mirrorEndpoint = "https://hf.example.internal";
    const mirrored = buildBody(state).spec as Record<string, unknown>;
    expect(mirrored.model_source).toEqual({ kind: "mirror", endpoint: "https://hf.example.internal" });
    expect(mirrored.model_cache).toEqual({ size: "200Gi" });
    state.fields = { ...state.fields, modelSource: "object_store", objectUri: "models/q" };
    expect(formError(state)).toMatch(/s3:\/\//);
    state.fields = { ...state.fields, objectUri: "s3://models/q", objectSecret: "model-store" };
    const streamed = buildBody(state).spec as Record<string, unknown>;
    expect([streamed.model_cache, streamed.model_source]).toEqual([
      null,
      { kind: "object_store", uri: "s3://models/q", credentials: { name: "model-store" } },
    ]);
  });

  it("runs SGLang when chosen, round-trips it on edit, and refuses SGLang streaming from a store", () => {
    const state = initialState();
    expect(state.server).toBe("vllm");
    state.fields = { ...state.fields, name: "s", namespace: "n", model: "m", image: "lmsysorg/sglang:v0.5.23" };
    state.server = "sglang";
    const spec = buildBody(state).spec as Record<string, unknown>;
    expect(spec.server).toBe("sglang");
    const stored = { ...managedTarget, spec: { ...managedTarget.spec, server: "sglang" } } as ManagedTarget;
    expect(initialState(stored).server).toBe("sglang");
    expect(initialState(hostTarget).server).toBe("vllm");
    state.fields = { ...state.fields, modelSource: "object_store", objectUri: "s3://models/q" };
    expect(formError(state)).toMatch(/SGLang cannot stream/);
  });

  it("moves a default image to the chosen server's, and keeps one the user typed", () => {
    const state = initialState(undefined, "kubernetes", "registry.example.internal:5000");
    const sglang = withServer(state, "sglang", "registry.example.internal:5000");
    expect(sglang.fields.image).toBe("registry.example.internal:5000/lmsysorg/sglang:v0.5.23-runtime");
    expect(withServer(sglang, "vllm", "registry.example.internal:5000").fields.image).toBe(state.fields.image);
    const typed = { ...state, fields: { ...state.fields, image: "my/vllm:custom" } };
    expect(withServer(typed, "sglang").fields.image).toBe("my/vllm:custom");
  });

  it("builds a new Kubernetes target, leaving cleared optional fields out", () => {
    const state = initialState();
    state.fields = {
      ...state.fields,
      name: " qwen ",
      namespace: "servtuner-tune-qwen",
      image: "vllm/vllm-openai:v0.34.0",
    };
    state.fields.model = "Qwen/Qwen3-32B";
    state.fields.baseFlags = "--max-model-len=32768\n\n";
    expect(buildBody(state)).toEqual({
      name: "qwen",
      namespace: "servtuner-tune-qwen",
      spec: {
        placement: "kubernetes",
        server: "vllm",
        image: "vllm/vllm-openai:v0.34.0",
        model: "Qwen/Qwen3-32B",
        gpus: 1,
        cpu: "8",
        memory: "64Gi",
        model_cache: { size: "200Gi" },
        transport: { enabled: false },
        base_flags: ["--max-model-len=32768"],
      },
    });
  });

  it("round-trips a host target, keeping its Secret references (pasted keys go elsewhere)", () => {
    const body = buildBody(initialState(hostTarget), hostTarget);
    expect(body).toEqual({
      name: "rtx",
      description: undefined,
      namespace: "vllm-lan",
      spec: hostTarget.spec,
    });
  });

  it("keeps a stored Secret key, issuer kind and storage class on edit", () => {
    const tls: ManagedTarget = {
      ...managedTarget,
      spec: {
        ...managedTarget.spec,
        placement: "kubernetes",
        hf_token: { name: "hf", key: "hf_token" },
        model_cache: { size: "200Gi", storage_class: "fast" },
        node_selector: { "nvidia.com/gpu.product": "NVIDIA-H200" },
        transport: {
          enabled: true,
          client_auth: "none",
          issuer: { kind: "Issuer", name: "local-ca", group: "cert-manager.io" },
        },
      } as ManagedTarget["spec"],
    };
    const state = initialState(tls);
    expect(state.transport).toBe("issuer");
    state.fields.cache = "300Gi";
    const spec = buildBody(state, tls).spec;
    expect(spec.hf_token).toEqual({ name: "hf", key: "hf_token" });
    expect(spec.model_cache).toEqual({ size: "300Gi", storage_class: "fast" });
    expect(spec.node_selector).toEqual({ "nvidia.com/gpu.product": "NVIDIA-H200" });
    expect(spec.transport).toEqual({
      enabled: true,
      issuer: { kind: "Issuer", name: "local-ca", group: "cert-manager.io" },
    });
  });

  it("starts a fresh spec when an edit changes the placement", () => {
    const state = { ...initialState(hostTarget), placement: "kubernetes" as const };
    state.fields = { ...state.fields, image: "vllm/vllm-openai:v0.34.0" };
    const spec = buildBody(state, hostTarget).spec;
    expect(spec.placement).toBe("kubernetes");
    expect(spec).not.toHaveProperty("launcher_url");
    expect(spec.served_model_name).toBe("tiny"); // a shown field carries over
  });

  it("reads env as NAME=value lines, and clears it on edit", () => {
    expect(parseKeyValues("VLLM_USE_V1=1\n\nA=b=c")).toEqual({ pairs: { VLLM_USE_V1: "1", A: "b=c" } });
    expect(parseKeyValues("NOPE")).toEqual({ error: "Write NAME=value: NOPE" });
    const withEnv: ManagedTarget = {
      ...managedTarget,
      spec: { ...managedTarget.spec, env: { VLLM_USE_V1: "1" } } as ManagedTarget["spec"],
    };
    const state = initialState(withEnv);
    expect(state.fields.env).toBe("VLLM_USE_V1=1");
    state.fields.env = "";
    expect(buildBody(state, withEnv).spec.env).toEqual({});
  });

  it("places a Kubernetes target with a node selector and tolerations, round-tripped on edit", () => {
    const gpuNode: ManagedTarget = {
      ...managedTarget,
      spec: {
        ...managedTarget.spec,
        node_selector: { "nvidia.com/gpu.product": "NVIDIA-H200" },
        tolerations: [{ key: "nvidia.com/gpu", operator: "Exists", effect: "NoSchedule" }],
      } as ManagedTarget["spec"],
    };
    const state = initialState(gpuNode);
    expect(state.fields.nodeSelector).toBe("nvidia.com/gpu.product=NVIDIA-H200");
    const spec = buildBody(state, gpuNode).spec;
    expect(spec.node_selector).toEqual({ "nvidia.com/gpu.product": "NVIDIA-H200" });
    expect(spec.tolerations).toEqual([{ key: "nvidia.com/gpu", operator: "Exists", effect: "NoSchedule" }]);
    state.fields.nodeSelector = "";
    state.fields.tolerations = "";
    expect(buildBody(state, gpuNode).spec).toMatchObject({ node_selector: {}, tolerations: [] });
  });

  it("keeps Save off while a free-text field does not parse", () => {
    const state = initialState();
    expect(formError(state)).toBeUndefined();
    expect(parseTolerations("key: x")).toHaveProperty("error");
    expect(formError({ ...state, fields: { ...state.fields, tolerations: "key: x" } })).toMatch(/YAML list/);
    expect(formError({ ...state, fields: { ...state.fields, nodeSelector: "gpu" } })).toBe("Write NAME=value: gpu");
    // A host has no node selector: its leftover text does not block it.
    expect(
      formError({ ...state, placement: "host", fields: { ...state.fields, nodeSelector: "gpu" } }),
    ).toBeUndefined();
  });

  it("sizes /dev/shm when asked, and clears a stored size on edit", () => {
    const fresh = initialState();
    expect(buildBody(fresh).spec).not.toHaveProperty("shm_size");
    fresh.fields.shmSize = " 16Gi ";
    expect(buildBody(fresh).spec.shm_size).toBe("16Gi");

    const sized: ManagedTarget = {
      ...managedTarget,
      spec: { ...managedTarget.spec, shm_size: "16Gi" } as ManagedTarget["spec"],
    };
    const editing = initialState(sized);
    expect(editing.fields.shmSize).toBe("16Gi");
    editing.fields.shmSize = "";
    expect(buildBody(editing, sized).spec.shm_size).toBeNull();
  });
});
