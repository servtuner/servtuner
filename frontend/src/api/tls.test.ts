import { describe, expect, it } from "vitest";

import { tlsBlock, tlsChoice } from "./tls";

const choice = (enabled: boolean, clientCertSecret = "", caSecret = "") => ({ enabled, clientCertSecret, caSecret });

describe("tlsBlock", () => {
  it("writes plain HTTP with no Secrets, whatever the fields hold", () => {
    expect(tlsBlock(choice(false, "client", "ca"))).toEqual({ enabled: false });
  });

  it("writes TLS with the Secrets that are named", () => {
    expect(tlsBlock(choice(true))).toEqual({ enabled: true });
    expect(tlsBlock(choice(true, " client "))).toEqual({
      enabled: true,
      clientCert: { secretRef: { name: "client" } },
    });
    expect(tlsBlock(choice(true, "", "ca"))).toEqual({ enabled: true, caSecretRef: { name: "ca" } });
    expect(tlsBlock(choice(true, "client", "ca"))).toEqual({
      enabled: true,
      clientCert: { secretRef: { name: "client" } },
      caSecretRef: { name: "ca" },
    });
  });
});

describe("tlsChoice", () => {
  it("reads a block back, so an edit round-trips", () => {
    for (const block of [
      { enabled: false },
      { enabled: true },
      { enabled: true, clientCert: { secretRef: { name: "client" } }, caSecretRef: { name: "ca" } },
    ]) {
      expect(tlsBlock(tlsChoice(block))).toEqual(block);
    }
  });

  it("defaults a missing block to TLS", () => {
    expect(tlsChoice(undefined)).toEqual(choice(true));
  });

  it("reads servtuner 0.1's mode, so an old document opens with its settings", () => {
    expect(tlsChoice({ mode: "platform" })).toEqual(choice(false));
    expect(tlsChoice({ mode: "mtls", secretRef: { name: "client" } })).toEqual(choice(true, "client"));
    expect(tlsChoice({ mode: "tls", caSecretRef: { name: "ca" } })).toEqual(choice(true, "", "ca"));
  });
});
