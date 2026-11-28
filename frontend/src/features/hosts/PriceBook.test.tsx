import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { PriceBook } from "../../api/types";
import { renderApp } from "../../test/render";
import { server, TEST_API_BASE_URL as API } from "../../test/server";
import { EMPTY_NODE, priceBookBody, priceBookError, priceBookFields, priceBookLine } from "./priceBook";

const STATIC: PriceBook = {
  source: "static",
  discount: 0.7,
  nodes: { "p5en.48xlarge": { usd_per_hour: 63.3, cores: 188, memory_gib: 1900, gpus: 8 } },
  gpus: { "NVIDIA H200": { usd_per_hour: 7.9 } },
};

describe("the price book form", () => {
  it("round-trips a book through its fields", () => {
    expect(priceBookBody(priceBookFields(STATIC))).toEqual(STATIC);
    expect(priceBookBody(priceBookFields({ source: "aws", discount: 1, aws_region: "eu-central-1" }))).toEqual({
      source: "aws",
      discount: 1,
      aws_region: "eu-central-1",
      nodes: {},
      gpus: {},
    });
  });

  it("says what stops it saving", () => {
    const fields = priceBookFields(STATIC);
    expect(priceBookError(fields)).toBeNull();
    expect(priceBookError({ ...fields, discount: "0" })).toMatch(/discount/);
    expect(priceBookError({ ...fields, source: "aws" })).toMatch(/AWS region/);
    expect(priceBookError({ ...fields, nodes: [], gpus: [] })).toMatch(/at least one/);
    expect(priceBookError({ ...fields, gpus: [...fields.gpus, ...fields.gpus] })).toMatch(/twice/);
    expect(priceBookError({ ...fields, nodes: [{ ...EMPTY_NODE, ...fields.nodes[0], cores: "" }] })).toMatch(/cores/);
    // A blank row is ignored, not refused.
    expect(priceBookError({ ...fields, gpus: [...fields.gpus, { key: "", usdPerHour: "" }] })).toBeNull();
  });

  it("describes the book in one line", () => {
    expect(priceBookLine(STATIC)).toBe("Price table: 1 node type and 1 GPU model, at 70% of list");
    expect(priceBookLine({ source: "aws", discount: 1, aws_region: "us-east-1" })).toBe("AWS Price List in us-east-1");
    expect(priceBookLine({ source: "none" })).toMatch(/^Not priced/);
  });
});

describe("the prices section", () => {
  it("prices GPUs from the managed server page", async () => {
    const user = userEvent.setup();
    let saved: PriceBook | undefined;
    server.use(
      http.put(`${API}/price-book`, async ({ request }) => {
        saved = (await request.json()) as PriceBook;
        return HttpResponse.json(saved);
      }),
    );
    renderApp("/managed-targets");
    expect(await screen.findByText(/^Not priced/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit prices" }));
    await user.click(screen.getByText("Price table"));
    const save = screen.getByRole("button", { name: "Save prices" });
    expect(save).toBeDisabled(); // nothing priced yet
    await user.click(screen.getByRole("button", { name: "Add a GPU model" }));
    await user.type(screen.getByLabelText("GPU model"), "NVIDIA H200");
    await user.type(screen.getByLabelText("USD per GPU-hour"), "7.9");
    await user.click(save);
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved).toEqual({ source: "static", discount: 1, nodes: {}, gpus: { "NVIDIA H200": { usd_per_hour: 7.9 } } });
  });

  it("uploads the operator's datasheet and shows what it holds", async () => {
    const user = userEvent.setup();
    let uploaded: string | undefined;
    server.use(
      http.put(`${API}/price-book/datasheet`, async ({ request }) => {
        uploaded = await request.text();
        return HttpResponse.json({
          source: "none",
          offline: true,
          datasheet: {
            source: "upload",
            sha256: "ab".repeat(32),
            size_bytes: 10,
            entries: 2,
            priced_entries: 1,
            uploaded_at: "2026-11-26T12:00:00Z",
          },
        });
      }),
      http.get(`${API}/price-book`, () => HttpResponse.json({ source: "none", offline: true })),
    );
    renderApp("/managed-targets");
    expect(await screen.findByText(/^No datasheet: this install is offline/)).toBeInTheDocument();
    const sheet = new File(['{"m": {"input_cost_per_token": 1e-7}}'], "prices.json", { type: "application/json" });
    const input = document.querySelector('input[type="file"]') as HTMLInputElement;
    await user.upload(input, sheet);
    await waitFor(() => expect(uploaded).toBe('{"m": {"input_cost_per_token": 1e-7}}'));
  });

  it("offers no AWS Price List offline", async () => {
    const user = userEvent.setup();
    server.use(http.get(`${API}/price-book`, () => HttpResponse.json({ source: "none", offline: true })));
    renderApp("/managed-targets");
    await user.click(await screen.findByRole("button", { name: "Edit prices" }));
    expect(screen.getByText("Price table")).toBeInTheDocument();
    expect(screen.queryByText("AWS Price List")).toBeNull();
  });

  it("keeps an internal datasheet URL in the book", () => {
    const fields = { ...priceBookFields(STATIC), datasheetUrl: "https://prices.example.internal/sheet.json" };
    expect(priceBookBody(fields).datasheet_url).toBe("https://prices.example.internal/sheet.json");
    expect(priceBookError({ ...fields, datasheetUrl: "prices.json" })).toMatch(/http/);
  });
});
