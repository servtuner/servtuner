import { Anchor, Text } from "@mantine/core";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { useRunDeletion } from "../../api/hooks";
import { ConfirmButton } from "../../components/ConfirmButton";
import { DeleteImpact } from "../../components/DeleteImpact";
import { plural } from "../../components/format";
import { QueryError } from "../../components/QueryError";

/** Delete this one run, only from its own page: there is no bulk delete. */
export function DeleteRunButton({ runId, name }: { runId: string; name: string }) {
  const [isOpen, setIsOpen] = useState(false);
  const { impact, remove } = useRunDeletion(runId, isOpen);
  const navigate = useNavigate();
  const blocks = impact.data?.blocks ?? [];
  const goes = impact.data
    ? [
        `the run and its ${plural(impact.data.bundle_files, "bundle file")}`,
        ...(impact.data.analyses.length > 0
          ? [
              `${plural(impact.data.analyses.length, "experiment comparison")} that included it ` +
                "(compare the experiment again to recompute)",
            ]
          : []),
      ]
    : [];
  return (
    <>
      <ConfirmButton
        label="Delete run"
        title={`Delete ${name}?`}
        message={
          <DeleteImpact
            isPending={impact.isPending}
            error={impact.error}
            blocks={blocks}
            goes={goes}
            notes={impact.data?.notes ?? []}
          >
            {impact.data && impact.data.bundle_files > 0 && (
              <Text size="sm">
                To keep any of its raw data, download it from{" "}
                <Anchor component={Link} to="?tab=files">
                  Files
                </Anchor>{" "}
                first.
              </Text>
            )}
          </DeleteImpact>
        }
        confirmLabel="Delete"
        isPending={remove.isPending}
        isConfirmDisabled={!impact.isSuccess || blocks.length > 0}
        onOpenChange={setIsOpen}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate("/runs") })}
      />
      {remove.isError && <QueryError title="Could not delete the run" error={remove.error} />}
    </>
  );
}
