import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { succeededRun } from "../test/fixtures";
import { renderApp } from "../test/render";

describe("router basename", () => {
  it("serves routes under the prefix and builds links under it", async () => {
    const { router } = renderApp("/a/b/c/runs", "/a/b/c");
    const link = await screen.findByRole("link", { name: succeededRun.name });
    expect(link).toHaveAttribute("href", `/a/b/c/runs/${succeededRun.id}`);
    expect(router.state.location.pathname).toBe("/a/b/c/runs");
  });

  it("serves the overview at the prefix root, linking under the prefix", async () => {
    const { router } = renderApp("/servtuner/", "/servtuner");
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/servtuner/");
    expect(await screen.findByRole("link", { name: succeededRun.name })).toHaveAttribute(
      "href",
      `/servtuner/runs/${succeededRun.id}`,
    );
  });
});
