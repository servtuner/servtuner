import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import { createApiClient } from "../api/client";
import { server } from "../test/server";
import { loadRuntimeConfig } from "./config";

describe("loadRuntimeConfig", () => {
  it.each([
    ["http://host.test/", "http://host.test/config.json", "http://host.test/api"],
    ["http://host.test/servtuner/", "http://host.test/servtuner/config.json", "http://host.test/servtuner/api"],
    ["http://host.test/a/b/c/", "http://host.test/a/b/c/config.json", "http://host.test/a/b/c/api"],
  ])("under %s fetches %s and resolves the relative API URL", async (baseUri, configUrl, apiBaseUrl) => {
    server.use(http.get(configUrl, () => HttpResponse.json({ apiBaseUrl: "api" })));
    await expect(loadRuntimeConfig(baseUri)).resolves.toEqual({ apiBaseUrl });
  });

  it("keeps an absolute API URL", async () => {
    server.use(
      http.get("http://host.test/servtuner/config.json", () =>
        HttpResponse.json({ apiBaseUrl: "https://api.example.test/v1/" }),
      ),
    );
    await expect(loadRuntimeConfig("http://host.test/servtuner/")).resolves.toEqual({
      apiBaseUrl: "https://api.example.test/v1",
    });
  });

  it("sends API requests under the prefix", async () => {
    let requestedUrl = "";
    server.use(
      http.get("http://host.test/servtuner/api/runs", ({ request }) => {
        requestedUrl = request.url;
        return HttpResponse.json([]);
      }),
    );
    await createApiClient("http://host.test/servtuner/api").listRuns({
      status: "running",
    });
    expect(requestedUrl).toBe("http://host.test/servtuner/api/runs?status=running");
  });
});
