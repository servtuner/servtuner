import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import type { Action, AuthStatus, AuthUser } from "../../api/types";
import { renderApp } from "../../test/render";
import { TEST_API_BASE_URL as API, server } from "../../test/server";

const VIEWER_ACTIONS: Action[] = ["read"];
const ADMIN_ACTIONS: Action[] = [
  "logs.read",
  "read",
  "runs.delete",
  "runs.write",
  "secrets.write",
  "targets.check",
  "targets.write",
  "tuning.write",
  "users.admin",
];

function user(overrides: Partial<AuthUser> = {}): AuthUser {
  return {
    id: "01USER0000000000000000000A",
    username: "ada",
    display_name: null,
    role: "admin",
    auth_method: "session",
    must_change_password: false,
    actions: ADMIN_ACTIONS,
    ...overrides,
  };
}

function local(overrides: Partial<AuthStatus> = {}): AuthStatus {
  return { mode: "local", methods: ["local"], setup_required: false, user: null, ...overrides };
}

/** Serve a status that tests change as the server would after setup or sign-in. */
function serveStatus(initial: AuthStatus) {
  const state = { status: initial };
  server.use(http.get(`${API}/auth/status`, () => HttpResponse.json<AuthStatus>(state.status)));
  return state;
}

describe("the auth gate", () => {
  it("creates the first admin with the setup token, then shows the app", async () => {
    const state = serveStatus(local({ setup_required: true }));
    const sent: { token: string | null; body: unknown }[] = [];
    server.use(
      http.post(`${API}/auth/setup`, async ({ request }) => {
        sent.push({ token: request.headers.get("X-Servtuner-Setup-Token"), body: await request.json() });
        state.status = local({ user: user() });
        return HttpResponse.json<AuthUser>(user(), { status: 201 });
      }),
    );
    const userAction = userEvent.setup();
    renderApp("/runs");
    await userAction.type(await screen.findByLabelText(/Setup token/), "the-token");
    await userAction.type(screen.getByLabelText(/Admin username/), "ada");
    await userAction.type(screen.getByLabelText(/^Password/), "correct horse battery");
    await userAction.type(screen.getByLabelText(/Repeat the password/), "correct horse battery");
    await userAction.click(screen.getByRole("button", { name: "Create admin" }));
    expect(await screen.findByRole("banner")).toBeInTheDocument();
    expect(sent).toEqual([{ token: "the-token", body: { username: "ada", password: "correct horse battery" } }]);
  });

  it("shows the server's message when sign-in fails, and the app once it works", async () => {
    const state = serveStatus(local());
    server.use(
      http.post(`${API}/auth/login`, async ({ request }) => {
        const body = (await request.json()) as { password: string };
        if (body.password !== "right password")
          return HttpResponse.json(
            { detail: { code: "invalid_credentials", message: "Invalid username or password" } },
            { status: 401 },
          );
        state.status = local({ user: user() });
        return HttpResponse.json<AuthUser>(user());
      }),
    );
    const userAction = userEvent.setup();
    renderApp("/runs");
    await userAction.type(await screen.findByLabelText(/Username/), "ada");
    await userAction.type(screen.getByLabelText(/Password/), "wrong password");
    await userAction.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid username or password");
    await userAction.clear(screen.getByLabelText(/Password/));
    await userAction.type(screen.getByLabelText(/Password/), "right password");
    await userAction.click(screen.getByRole("button", { name: "Sign in" }));
    expect(await screen.findByRole("banner")).toBeInTheDocument();
  });

  it("tells a proxy user with no account who the proxy said they are", async () => {
    serveStatus(local({ methods: ["forward_header"], refused_subject: "CN=Jo,O=Example" }));
    renderApp("/runs");
    expect(await screen.findByText(/CN=Jo,O=Example has no servtuner account/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  });

  it("asks for a new password after a reset, before anything else", async () => {
    serveStatus(local({ user: user({ must_change_password: true }) }));
    renderApp("/runs");
    expect(await screen.findByRole("heading", { name: "Choose a new password" })).toBeInTheDocument();
    expect(screen.queryByRole("banner")).toBeNull();
  });

  it("goes back to sign-in when a session ends", async () => {
    const state = serveStatus(local({ user: user() }));
    server.use(
      http.get(`${API}/runs`, () => {
        state.status = local();
        return HttpResponse.json({ detail: { code: "session_ended", message: "sign in again" } }, { status: 401 });
      }),
    );
    renderApp("/runs");
    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
  });
});

describe("what a role sees", () => {
  it("shows an admin the Users page and Sign out", async () => {
    serveStatus(local({ user: user() }));
    const userAction = userEvent.setup();
    renderApp("/runs");
    await userAction.click(await screen.findByRole("button", { name: "Account menu" }));
    expect(await screen.findByRole("menuitem", { name: "Users", hidden: true })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Sign out", hidden: true })).toBeInTheDocument();
  });

  it("hides New run and Users from a viewer, and Sign out from a proxy user", async () => {
    serveStatus(local({ user: user({ role: "viewer", actions: VIEWER_ACTIONS, auth_method: "forward_header" }) }));
    const userAction = userEvent.setup();
    renderApp("/runs");
    const header = await screen.findByRole("banner");
    await screen.findByRole("heading", { name: "Runs" });
    expect(screen.queryByRole("link", { name: "New run" })).toBeNull();
    await userAction.click(within(header).getByRole("button", { name: "Account menu" }));
    await screen.findByRole("menuitem", { name: "Account", hidden: true });
    expect(screen.queryByRole("menuitem", { name: "Users", hidden: true })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Sign out", hidden: true })).toBeNull();
  });

  it("shows everything under auth.mode none, with no account menu", async () => {
    renderApp("/runs");
    await waitFor(() => expect(screen.getByRole("link", { name: "New run" })).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Account menu" })).toBeNull();
  });
});
