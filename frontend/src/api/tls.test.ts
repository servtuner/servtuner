import { describe, expect, it } from "vitest";
import { tlsBlock } from "./tls";

describe("tlsBlock", () => {
  it("writes each mode's own settings and nothing else", () => {
    expect(tlsBlock("platform", "ignored")).toEqual({ mode: "platform" });
    expect(tlsBlock("mtls", "servtuner-client")).toEqual({ mode: "mtls", secretRef: { name: "servtuner-client" } });
    expect(tlsBlock("tls", "vllm-ca")).toEqual({ mode: "tls", caSecretRef: { name: "vllm-ca" } });
  });

  it("leaves out an unnamed CA Secret in tls mode", () => {
    expect(tlsBlock("tls", "")).toEqual({ mode: "tls" });
  });
});
