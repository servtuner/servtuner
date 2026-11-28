import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { Host, HostCreate } from "../../api/types";
import { host } from "../../test/fixtures";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL as API } from "../../test/server";

describe("hosts", () => {
  it("are listed with the managed servers and added with an owned-hardware price", async () => {
    const user = userEvent.setup();
    let created: HostCreate | undefined;
    server.use(
      http.post(`${API}/hosts`, async ({ request }) => {
        created = (await request.json()) as HostCreate;
        return HttpResponse.json<Host>({ ...host, id: "01JHOSTNEW0000000000000000" }, { status: 201 });
      }),
    );
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Add a host" }));
    const save = screen.getByRole("button", { name: "Save host" });
    expect(save).toBeDisabled(); // no name yet
    await user.type(screen.getByLabelText(/^Name/), "gpu-box");
    await user.type(screen.getByLabelText("GPU model"), "NVIDIA GeForce RTX 3070");
    await user.type(screen.getByLabelText("GPU memory (GiB)"), "8");
    await user.click(screen.getByText("Owned hardware"));
    await user.type(screen.getByLabelText("Purchase (USD)"), "500");
    await user.type(screen.getByLabelText(/^Spread over/), "8760");
    await user.type(screen.getByLabelText(/^Power/), "220");
    await user.type(screen.getByLabelText("Electricity (USD/kWh)"), "0.087");
    await user.click(save);
    await waitFor(() => expect(created).toBeDefined());
    expect(created).toEqual({
      name: "gpu-box",
      note: null,
      hardware: {
        source: "manual",
        gpus: [{ model: "NVIDIA GeForce RTX 3070", memory_bytes: 8589934592, driver_version: null }],
        cpu_model: null,
        cpu_cores: null,
        memory_bytes: null,
        read_at: null,
      },
      price: { kind: "owned", purchase_usd: 500, amortization_hours: 8760, watts: 220, usd_per_kwh: 0.087 },
    });
  }, 15_000); // types a whole form

  it("shows what the machine is and costs, reads it from the launcher, and is kept while used", async () => {
    const user = userEvent.setup();
    let reads = 0;
    const used = { ...host, target_ids: ["01JTARGET00000000000000000"] };
    server.use(
      http.get(`${API}/hosts/:id`, () => HttpResponse.json<Host>(used)),
      http.post(`${API}/hosts/:id/read-machine`, () => {
        reads += 1;
        return HttpResponse.json<Host>(used);
      }),
    );
    renderApp(`/hosts/${host.id}`);
    expect(await screen.findByRole("heading", { name: "gpu-box" })).toBeInTheDocument();
    expect(screen.getByText("1 × NVIDIA GeForce RTX 3070 (8 GiB) · 16 CPU cores · 32 GiB")).toBeInTheDocument();
    expect(screen.getByText(/\$0.0762\/h \(500 USD ÷ 8760 h/)).toBeInTheDocument();
    expect(screen.getByText(/Read from the launcher/, { selector: "p" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Read from the launcher" }));
    await waitFor(() => expect(reads).toBe(1));
    await user.click(screen.getByRole("button", { name: "Delete host" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/point them at another host first/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Delete" })).toBeDisabled();
  });

  it("cannot be read from a launcher before a managed server runs on it", async () => {
    server.use(http.get(`${API}/hosts/:id`, () => HttpResponse.json<Host>(host)));
    renderApp(`/hosts/${host.id}`);
    expect(await screen.findByRole("button", { name: "Read from the launcher" })).toBeDisabled();
  });
});
