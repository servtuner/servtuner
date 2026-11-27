import { render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { AuthStatus, ListRunsResponse } from "../api/types";
import { succeededRun } from "../test/fixtures";
import { authOff, server } from "../test/server";
import { App } from "./App";
import { loadRuntimeConfig } from "./config";

// The real startup path under a context path: <base href> -> config.json ->
// router basename -> API URL. nginx emits a path-only href, so does this test.
describe("App under a context path", () => {
  let baseElement: HTMLBaseElement;

  beforeEach(() => {
    baseElement = document.createElement("base");
    baseElement.href = "/a/b/";
    document.head.appendChild(baseElement);
    window.history.pushState({}, "", "/a/b/runs");
  });

  afterEach(() => {
    baseElement.remove();
    window.history.pushState({}, "", "/");
  });

  it("loads config.json, routes and calls the API under the prefix", async () => {
    expect(document.baseURI).toBe(`${window.location.origin}/a/b/`);
    const requestedUrls: string[] = [];
    server.use(
      http.get(`${window.location.origin}/a/b/config.json`, () => HttpResponse.json({ apiBaseUrl: "api" })),
      http.get(`${window.location.origin}/a/b/api/auth/status`, () => HttpResponse.json<AuthStatus>(authOff)),
      http.get(`${window.location.origin}/a/b/api/runs`, ({ request }) => {
        requestedUrls.push(request.url);
        return HttpResponse.json<ListRunsResponse>([succeededRun]);
      }),
    );

    render(<App config={await loadRuntimeConfig()} />);

    const link = await screen.findByRole("link", { name: succeededRun.name });
    expect(link).toHaveAttribute("href", `/a/b/runs/${succeededRun.id}`);
    expect(screen.getByRole("link", { name: "servtuner" })).toHaveAttribute("href", "/a/b");
    expect(requestedUrls).toEqual([`${window.location.origin}/a/b/api/runs`]);
  });
});
