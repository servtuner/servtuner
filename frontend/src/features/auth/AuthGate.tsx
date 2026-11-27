import { Alert, Center, Loader } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect } from "react";
import { AUTH_STATUS_KEY, useAuthStatus } from "../../api/auth";
import { ApiError } from "../../api/client";
import { AuthScreen } from "./AuthScreen";
import { ChangePasswordForm } from "./ChangePasswordForm";
import { LoginPage } from "./LoginPage";
import { SetupPage } from "./SetupPage";

/** A 401 anywhere means the session ended: ask the server what to show again. */
function useRecheckOnUnauthorized() {
  const queryClient = useQueryClient();
  useEffect(() => {
    const recheck = (error: unknown) => {
      if (error instanceof ApiError && error.status === 401)
        void queryClient.invalidateQueries({ queryKey: AUTH_STATUS_KEY });
    };
    const unsubscribeQueries = queryClient.getQueryCache().subscribe((event) => {
      if (event.type === "updated" && event.action.type === "error" && event.query.queryKey[0] !== AUTH_STATUS_KEY[0])
        recheck(event.action.error);
    });
    const unsubscribeMutations = queryClient.getMutationCache().subscribe((event) => {
      if (event.type === "updated" && event.action.type === "error") recheck(event.action.error);
    });
    return () => {
      unsubscribeQueries();
      unsubscribeMutations();
    };
  }, [queryClient]);
}

/** Setup, sign-in, a refused proxy identity, a forced password change, or the app.
 * With auth.mode none, it shows the app as before. */
export function AuthGate({ children }: { children: ReactNode }) {
  const status = useAuthStatus();
  useRecheckOnUnauthorized();
  if (status.isPending)
    return (
      <Center mih="100vh">
        <Loader aria-label="Loading" />
      </Center>
    );
  if (status.isError)
    return (
      <Alert color="red" title="servtuner could not reach its api" m="md" role="alert">
        {status.error.message}
      </Alert>
    );
  const { data } = status;
  if (data.mode === "none") return children;
  if (data.setup_required) return <SetupPage status={data} />;
  if (data.user === null) {
    if (data.methods.includes("local")) return <LoginPage refusedSubject={data.refused_subject} />;
    return (
      <AuthScreen
        title="No servtuner account"
        intro={
          data.refused_subject
            ? `Your identity ${data.refused_subject} has no servtuner account. Ask an admin to link it on the Users page.`
            : "The proxy in front of servtuner did not say who you are."
        }
      />
    );
  }
  if (data.user.must_change_password)
    return (
      <AuthScreen title="Choose a new password" intro="Your password was reset; choose your own before going on.">
        <ChangePasswordForm />
      </AuthScreen>
    );
  return children;
}
