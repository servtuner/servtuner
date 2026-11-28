import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { renderApp } from "../test/render";

describe("navigation", () => {
  it("keeps the main job up top and the rest under More", async () => {
    const user = userEvent.setup();
    const { router } = renderApp("/runs");
    const header = await screen.findByRole("banner");
    for (const page of ["Managed Servers", "Tuning", "Runs", "Experiments"])
      expect(within(header).getByRole("link", { name: page })).toBeInTheDocument();
    expect(within(header).queryByRole("link", { name: "Planner" })).toBeNull();

    await user.click(within(header).getByRole("button", { name: "More pages" }));
    for (const page of ["Planner", "Schedules", "Targets"])
      expect(await screen.findByRole("menuitem", { name: page, hidden: true })).toBeInTheDocument();
    await user.click(screen.getByRole("menuitem", { name: "Schedules", hidden: true }));
    expect(router.state.location.pathname).toBe("/schedules");
  });
});
