import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { AuditEvent, TemporaryPassword, UserAdmin, UserCreate } from "../../api/types";
import { renderApp } from "../../test/render";
import { TEST_API_BASE_URL as API, server } from "../../test/server";

const operator: UserAdmin = {
  id: "01USER0000000000000000000B",
  username: "ops",
  display_name: null,
  email: null,
  role: "operator",
  has_password: true,
  must_change_password: false,
  is_disabled: false,
  created_at: "2026-11-26T12:00:00Z",
  identities: [
    { provider: "forward_header", subject: "CN=Ops,O=Example", created_at: "2026-11-26T12:00:00Z", last_seen_at: null },
  ],
};

function serveUsers(users: UserAdmin[], audit: AuditEvent[] = []) {
  server.use(
    http.get(`${API}/users`, () => HttpResponse.json<UserAdmin[]>(users)),
    http.get(`${API}/audit`, () => HttpResponse.json<AuditEvent[]>(audit)),
  );
}

describe("the users page", () => {
  it("lists users with their roles and proxy identities", async () => {
    serveUsers([operator]);
    renderApp("/users");
    const row = (await screen.findByText("ops")).closest("tr");
    expect(row).not.toBeNull();
    expect(within(row as HTMLElement).getByText("CN=Ops,O=Example")).toBeInTheDocument();
    expect(within(row as HTMLElement).getByLabelText("Role of ops")).toHaveValue("operator");
  });

  it("adds a user with one proxy identity per line", async () => {
    serveUsers([]);
    const created: UserCreate[] = [];
    server.use(
      http.post(`${API}/users`, async ({ request }) => {
        created.push((await request.json()) as UserCreate);
        return HttpResponse.json<UserAdmin>(operator, { status: 201 });
      }),
    );
    const userAction = userEvent.setup();
    renderApp("/users");
    await userAction.type(await screen.findByLabelText(/^Username/), "new-person");
    await userAction.type(screen.getByLabelText(/Proxy identities/), "CN=A, O=Example{enter}a@example.com");
    await userAction.click(screen.getByRole("button", { name: "Add user" }));
    await screen.findByRole("button", { name: "Add user" });
    expect(created).toEqual([
      { username: "new-person", role: "viewer", password: null, subjects: ["CN=A, O=Example", "a@example.com"] },
    ]);
  });

  it("shows a reset's temporary password once", async () => {
    serveUsers([operator]);
    server.use(
      http.post(`${API}/users/:id/password`, () =>
        HttpResponse.json<TemporaryPassword>({ password: "temporary-secret-123" }),
      ),
    );
    const userAction = userEvent.setup();
    renderApp("/users");
    await userAction.click(await screen.findByRole("button", { name: "Reset password" }));
    expect(await screen.findByText("temporary-secret-123")).toBeInTheDocument();
  });
});
