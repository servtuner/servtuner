import { Alert, Button, Code, Stack } from "@mantine/core";
import type { useManagedTargetActions } from "../../api/hooks";
import type { LauncherCheck } from "../../api/types";

const CHECK_STYLE: Record<LauncherCheck["status"], { color: string; title: string }> = {
  ok: { color: "teal", title: "Launcher reached" },
  unreachable: { color: "red", title: "Launcher unreachable" },
  refused: { color: "red", title: "Token refused" },
  no_token: { color: "yellow", title: "No launcher token" },
  error: { color: "red", title: "Launcher error" },
};

/** A host target's servtuner launcher, checked with its token before a deploy depends on it. */
export function LauncherCheckPanel({ check }: { check: ReturnType<typeof useManagedTargetActions>["checkLauncher"] }) {
  const result = check.data;
  return (
    <Stack gap="xs" maw={900}>
      <div>
        <Button variant="default" size="xs" loading={check.isPending} onClick={() => check.mutate()}>
          Check launcher
        </Button>
      </div>
      {check.isError && (
        <Alert color="red" title="Could not check the launcher">
          {check.error.message}
        </Alert>
      )}
      {result && (
        <Alert color={CHECK_STYLE[result.status].color} title={CHECK_STYLE[result.status].title}>
          {result.message}
          {result.vllm_args && result.vllm_args.length > 0 && (
            <Code block mt="xs" aria-label="Launcher vLLM args">
              {result.vllm_args.join(" ")}
            </Code>
          )}
        </Alert>
      )}
    </Stack>
  );
}
