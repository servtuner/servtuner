/**
 * A RunSpec hop's TLS, as the forms edit it: TLS's two questions. Is the connection encrypted,
 * and which Secrets does it use: a client certificate to present, and a private CA that signed
 * the server. Empty Secret names mean none.
 */
export interface TlsChoice {
  enabled: boolean;
  /** A kubernetes.io/tls Secret whose certificate servtuner presents; empty presents none. */
  clientCertSecret: string;
  /** A Secret whose ca.crt verifies the server; empty trusts the system pool. */
  caSecret: string;
}

/** The RunSpec tls block of a choice. Without TLS it names no Secrets, as the schema requires. */
export function tlsBlock(choice: TlsChoice): Record<string, unknown> {
  if (!choice.enabled) return { enabled: false };
  const block: Record<string, unknown> = { enabled: true };
  const clientCert = choice.clientCertSecret.trim();
  const ca = choice.caSecret.trim();
  if (clientCert) block.clientCert = { secretRef: { name: clientCert } };
  if (ca) block.caSecretRef = { name: ca };
  return block;
}

interface TlsBlockShape {
  enabled?: unknown;
  clientCert?: { secretRef?: { name?: unknown } };
  caSecretRef?: { name?: unknown };
  // servtuner 0.1's shape, read so an old document opens with its settings.
  mode?: unknown;
  secretRef?: { name?: unknown };
}

const asText = (value: unknown) => (typeof value === "string" ? value : "");

/** The choice a tls block makes; a missing block is TLS with no Secrets, the safe default. */
export function tlsChoice(block: unknown): TlsChoice {
  const tls = (block ?? {}) as TlsBlockShape;
  if (tls.mode !== undefined) {
    return {
      enabled: tls.mode !== "platform",
      clientCertSecret: tls.mode === "mtls" ? asText(tls.secretRef?.name) : "",
      caSecret: asText(tls.caSecretRef?.name),
    };
  }
  return {
    enabled: tls.enabled !== false,
    clientCertSecret: asText(tls.clientCert?.secretRef?.name),
    caSecret: asText(tls.caSecretRef?.name),
  };
}
