/** The transport modes a RunSpec hop can declare. */
export type TlsMode = "platform" | "mtls" | "tls";

/**
 * The RunSpec tls block of a mode. `secret` is the client certificate Secret
 * for mtls and the optional private CA Secret for tls; platform takes none.
 */
export function tlsBlock(mode: TlsMode, secret: string): Record<string, unknown> {
  if (mode === "mtls") return { mode: "mtls", secretRef: { name: secret } };
  if (mode === "tls") return secret ? { mode: "tls", caSecretRef: { name: secret } } : { mode: "tls" };
  return { mode: "platform" };
}
