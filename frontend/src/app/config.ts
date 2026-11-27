import { resolveAgainstBase } from "./basePath";

export interface RuntimeConfig {
  // Absolute once loaded. config.json may give it relative to the base URI (default "api") or absolute.
  apiBaseUrl: string;
}

// config.json is served next to index.html and is not cached, so one image
// works in any environment (frontend/CLAUDE.md).
export async function loadRuntimeConfig(baseUri: string = document.baseURI): Promise<RuntimeConfig> {
  const configUrl = resolveAgainstBase("config.json", baseUri);
  const response = await fetch(configUrl, { cache: "no-store" });
  if (!response.ok) throw new Error(`load ${configUrl}: HTTP ${response.status}`);
  const config: unknown = await response.json();
  if (
    typeof config !== "object" ||
    config === null ||
    !("apiBaseUrl" in config) ||
    typeof config.apiBaseUrl !== "string"
  ) {
    throw new Error(`${configUrl} must be an object with a string "apiBaseUrl"`);
  }
  return {
    apiBaseUrl: resolveAgainstBase(config.apiBaseUrl, baseUri).replace(/\/+$/, ""),
  };
}
