import { Checkbox } from "@mantine/core";
import { useState } from "react";
import { useNavigate } from "react-router";
import { useExperimentDeletion } from "../../api/hooks";
import { ConfirmButton } from "../../components/ConfirmButton";
import { DeleteImpact } from "../../components/DeleteImpact";
import { plural } from "../../components/format";
import { QueryError } from "../../components/QueryError";

/**
 * Delete an experiment with its runs, comparisons and finished tuning sessions. Saved
 * scenarios built on its runs go only when ticked; until then they block the delete.
 */
export function DeleteExperimentButton({ name }: { name: string }) {
  const [isOpen, setIsOpen] = useState(false);
  const [withScenarios, setWithScenarios] = useState(false);
  const { impact, remove } = useExperimentDeletion(name, isOpen);
  const navigate = useNavigate();
  const data = impact.data;
  const scenarios = data?.scenarios ?? [];
  const blocks = data?.blocks ?? [];
  const goes = data
    ? [
        `${plural(data.runs.length, "run")} and their ${plural(data.bundle_files, "bundle file")}`,
        ...(data.analyses > 0 ? [plural(data.analyses, "comparison")] : []),
        ...(data.sessions.length > 0 ? [plural(data.sessions.length, "finished tuning session")] : []),
        ...(withScenarios ? [plural(scenarios.length, "saved scenario")] : []),
      ]
    : [];
  return (
    <>
      <ConfirmButton
        label="Delete experiment"
        title={`Delete ${name}?`}
        message={
          <DeleteImpact
            isPending={impact.isPending}
            error={impact.error}
            blocks={blocks}
            goes={goes}
            notes={data?.notes ?? []}
          >
            {scenarios.length > 0 && (
              <Checkbox
                checked={withScenarios}
                onChange={(event) => setWithScenarios(event.currentTarget.checked)}
                label={`Also delete the saved scenarios built on its runs: ${scenarios.map((s) => s.name).join(", ")}`}
                description="They cannot be recomputed without these runs, so the experiment cannot go without them."
              />
            )}
          </DeleteImpact>
        }
        confirmLabel="Delete"
        isPending={remove.isPending}
        isConfirmDisabled={!impact.isSuccess || blocks.length > 0 || (scenarios.length > 0 && !withScenarios)}
        onOpenChange={(open) => {
          setIsOpen(open);
          if (!open) setWithScenarios(false);
        }}
        onConfirm={() => remove.mutate(withScenarios, { onSuccess: () => navigate("/experiments") })}
      />
      {remove.isError && <QueryError title="Could not delete the experiment" error={remove.error} />}
    </>
  );
}
