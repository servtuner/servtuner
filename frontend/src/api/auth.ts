import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useApiClient } from "./hooks";
import type { Action, LoginRequest, PasswordChange, SetupRequest, UserCreate, UserUpdate } from "./types";

// User auth. The server is the only authority: these hooks only decide what
// the UI shows.

export const AUTH_STATUS_KEY = ["auth-status"] as const;

export function useAuthStatus() {
  const client = useApiClient();
  return useQuery({ queryKey: AUTH_STATUS_KEY, queryFn: () => client.getAuthStatus(), staleTime: 60_000 });
}

/** Whether the signed-in user may do `action`; auth.mode none allows everything. While the
 * status loads nothing is hidden, since a wrong guess only costs a 403 the page shows. */
export function useCan(action: Action): boolean {
  const { data: status } = useAuthStatus();
  if (status === undefined || status.mode === "none") return true;
  return status.user?.actions?.includes(action) ?? false;
}

/** After signing in or out every cached answer may be someone else's, so all of it goes. */
function useResetAfterAuthChange() {
  const queryClient = useQueryClient();
  return () => queryClient.resetQueries();
}

export function useLogin() {
  const client = useApiClient();
  const reset = useResetAfterAuthChange();
  return useMutation({ mutationFn: (body: LoginRequest) => client.login(body), onSuccess: reset });
}

export function useLogout() {
  const client = useApiClient();
  const reset = useResetAfterAuthChange();
  return useMutation({ mutationFn: () => client.logout(), onSuccess: reset });
}

export function useSetup() {
  const client = useApiClient();
  const reset = useResetAfterAuthChange();
  return useMutation({
    mutationFn: ({ body, setupToken }: { body: SetupRequest; setupToken: string }) => client.setup(body, setupToken),
    onSuccess: reset,
  });
}

export function useChangePassword() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: PasswordChange) => client.changePassword(body),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: AUTH_STATUS_KEY }),
  });
}

export function useMySessions() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["my-sessions"] });
  return {
    sessions: useQuery({ queryKey: ["my-sessions"], queryFn: () => client.listMySessions() }),
    revoke: useMutation({ mutationFn: (id: string) => client.revokeMySession(id), onSuccess: refresh }),
    revokeOthers: useMutation({ mutationFn: () => client.revokeMyOtherSessions(), onSuccess: refresh }),
  };
}

export function useUsers() {
  const client = useApiClient();
  const queryClient = useQueryClient();
  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["users"] }),
      queryClient.invalidateQueries({ queryKey: ["audit"] }),
    ]);
  return {
    users: useQuery({ queryKey: ["users"], queryFn: () => client.listUsers() }),
    create: useMutation({ mutationFn: (body: UserCreate) => client.createUser(body), onSuccess: refresh }),
    update: useMutation({
      mutationFn: ({ id, body }: { id: string; body: UserUpdate }) => client.updateUser(id, body),
      onSuccess: refresh,
    }),
    resetPassword: useMutation({ mutationFn: (id: string) => client.resetUserPassword(id), onSuccess: refresh }),
    link: useMutation({
      mutationFn: ({ id, subject }: { id: string; subject: string }) => client.linkUserIdentity(id, { subject }),
      onSuccess: refresh,
    }),
    unlink: useMutation({
      mutationFn: ({ id, subject }: { id: string; subject: string }) => client.unlinkUserIdentity(id, subject),
      onSuccess: refresh,
    }),
    revokeSessions: useMutation({ mutationFn: (id: string) => client.revokeUserSessions(id), onSuccess: refresh }),
  };
}

export function useAudit() {
  const client = useApiClient();
  return useQuery({ queryKey: ["audit"], queryFn: () => client.listAudit() });
}
