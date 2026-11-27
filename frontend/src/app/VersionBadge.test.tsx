import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { createApiClient } from "../api/client";
import { ApiClientContext } from "../api/hooks";
import { renderApp } from "../test/render";
import { server, TEST_API_BASE_URL } from "../test/server";
import { theme } from "./theme";
import { VersionBadge } from "./VersionBadge";
import { shortCommit, versionsDiffer } from "./version";

const COMMIT = "0123456789abcdef0123456789abcdef01234567";

function apiVersion(version: string) {
  server.use(http.get(`${TEST_API_BASE_URL}/version`, () => HttpResponse.json({ version, commit: COMMIT })));
}

function renderBadge(uiVersion: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MantineProvider theme={theme}>
      <ApiClientContext.Provider value={createApiClient(TEST_API_BASE_URL)}>
        <QueryClientProvider client={queryClient}>
          <VersionBadge uiVersion={uiVersion} />
        </QueryClientProvider>
      </ApiClientContext.Provider>
    </MantineProvider>,
  );
}

describe("versionsDiffer", () => {
  it("flags two different releases only", () => {
    expect(versionsDiffer("0.2.0", "0.2.1")).toBe(true);
    expect(versionsDiffer("0.2.0", "0.2.0")).toBe(false);
    expect(versionsDiffer("0.0.0-dev", "0.2.0")).toBe(false);
    expect(versionsDiffer("0.2.0", "0.0.0-dev")).toBe(false);
  });

  it("shortens a full commit and keeps anything else", () => {
    expect(shortCommit(COMMIT)).toBe("0123456");
    expect(shortCommit("unknown")).toBe("unknown");
  });
});

describe("VersionBadge", () => {
  it("shows the api's version in the header", async () => {
    apiVersion("0.2.0");
    renderApp("/runs");
    const header = await screen.findByRole("banner");
    expect(await within(header).findByText("v0.2.0")).toBeInTheDocument();
  });

  it("warns when the UI and the api come from different releases", async () => {
    apiVersion("0.2.1");
    renderBadge("0.2.0");
    expect(await screen.findByText("UI 0.2.0 ≠ API 0.2.1")).toBeInTheDocument();
  });

  it("stays quiet when both are the same release", async () => {
    apiVersion("0.2.0");
    renderBadge("0.2.0");
    expect(await screen.findByText("v0.2.0")).toBeInTheDocument();
    expect(screen.queryByText(/≠/)).toBeNull();
  });
});
