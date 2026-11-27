import { Badge, Button, Checkbox, Group, Select, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { Link } from "react-router";
import { useCheckUnsavedTarget, useTargets } from "../../api/hooks";
import { CheckResults } from "../../components/CheckResults";

type Path = (string | number)[];
export type Change = [Path, unknown];

/** Runs made from a saved target carry its id here (api targets.TARGET_LABEL). */
export const TARGET_LABEL = "servtuner.target";
const CUSTOM = "custom";

/**
 * Pick a saved target, which writes its target block into
 * the RunSpec, and check the document's current target before creating a run.
 */
export function TargetPicker({
  selected,
  target,
  prometheus,
  edit,
}: {
  selected: string | undefined;
  target: unknown;
  prometheus: unknown;
  edit: (changes: Change[]) => void;
}) {
  const targets = useTargets();
  const check = useCheckUnsavedTarget();
  const [generate, setGenerate] = useState(false);
  const saved = targets.data?.find((one) => one.id === selected);
  const options = [
    { value: CUSTOM, label: "Custom (edit below or in the YAML)" },
    ...(targets.data ?? []).map((one) => ({ value: one.id, label: `${one.name} (${one.type})` })),
  ];

  return (
    <Stack gap="sm">
      <Group align="end" wrap="wrap">
        <Select
          label="Saved target"
          description={
            <>
              Fills the target block below. <Link to="/targets">Manage targets</Link>
            </>
          }
          data={options}
          value={saved ? saved.id : CUSTOM}
          allowDeselect={false}
          w={360}
          onChange={(value) => {
            check.reset();
            const picked = targets.data?.find((one) => one.id === value);
            if (!picked) {
              edit([[["labels", TARGET_LABEL], undefined]]);
              return;
            }
            // Its prices come with it: its hardware in the target, its blocks in analysis.
            const prices = picked.run_analysis ?? {};
            edit([
              [["target"], picked.run_target],
              [["labels", TARGET_LABEL], picked.id],
              ...(["infra_pricing", "token_pricing"] as const)
                .filter((name) => prices[name] !== undefined)
                .map((name): Change => [["analysis", name], prices[name]]),
            ]);
          }}
        />
        {saved &&
          (saved.credentials.api_key?.is_set ? (
            <Badge color="teal" variant="light">
              API key set
            </Badge>
          ) : (
            <Badge color="gray" variant="light">
              No API key
            </Badge>
          ))}
      </Group>
      <Group>
        <Button
          variant="default"
          loading={check.isPending}
          disabled={!target || typeof target !== "object"}
          onClick={() =>
            check.mutate({
              target: target as Record<string, unknown>,
              ...(prometheus && typeof prometheus === "object"
                ? { prometheus: prometheus as Record<string, unknown> }
                : {}),
              generate,
            })
          }
        >
          Check connection
        </Button>
        <Checkbox
          label="Also send one test request"
          checked={generate}
          onChange={(event) => setGenerate(event.currentTarget.checked)}
        />
      </Group>
      {check.isError && (
        <Text size="sm" c="red" role="alert">
          {check.error.message}
        </Text>
      )}
      {check.data && <CheckResults view={check.data} />}
    </Stack>
  );
}
