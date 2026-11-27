import {
  Checkbox,
  Group,
  Input,
  MultiSelect,
  NumberInput,
  Paper,
  SegmentedControl,
  Select,
  Stack,
  TagsInput,
} from "@mantine/core";
import { useState } from "react";
import type { KnobDescription } from "../../api/types";
import { DoneButton, KnobChip, KnobHelp, KnobLimits, KnobPicker } from "../knobs/KnobEditor";
import { type KnobValue, NUMERIC_KINDS } from "../knobs/knobValues";
import { describeTuned, isRange, newTunedKnob, type Tier, TIERS, type TunedKnob, tunedProblem } from "./spaceKnobs";

/**
 * A search space's tuned knobs as chips ("KV cache data type auto or fp8", "Max sequences 8–128"):
 * click one to set its stage and its values or bounds; "Add a knob" offers the whole catalog.
 * Servtuner propose checks the space when the session starts.
 */
export function SpaceKnobEditor({
  knobs,
  catalog,
  exclude,
  onChange,
}: {
  knobs: TunedKnob[];
  catalog: Map<string, KnobDescription>;
  exclude: ReadonlySet<string>;
  onChange: (knobs: TunedKnob[]) => void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const open = knobs.find((knob) => knob.name === editing);
  const label = "Tuned knobs";
  return (
    <Input.Wrapper label={label} description="What the session varies, stage by stage">
      <Group gap="xs" mt={6} role="list" aria-label={label}>
        {knobs.map((knob) => (
          <KnobChip
            key={knob.name}
            knob={catalog.get(knob.name)}
            name={knob.name}
            value={describeTuned(knob, catalog.get(knob.name))}
            problem={tunedProblem(knob, catalog.get(knob.name))}
            isEditing={editing === knob.name}
            onEdit={() => setEditing(editing === knob.name ? null : knob.name)}
            onRemove={() => {
              if (editing === knob.name) setEditing(null);
              onChange(knobs.filter((other) => other.name !== knob.name));
            }}
          />
        ))}
        <KnobPicker
          catalog={catalog}
          taken={new Set([...knobs.map((knob) => knob.name), ...exclude])}
          onPick={(name) => {
            const knob = catalog.get(name);
            if (!knob) return;
            onChange([...knobs, newTunedKnob(knob)]);
            setEditing(name);
          }}
        />
      </Group>
      {open && (
        <Paper withBorder p="sm" mt="xs" maw={480}>
          <TunedKnobForm
            key={open.name}
            knob={open}
            description={catalog.get(open.name)}
            onSet={(changed) => onChange(knobs.map((knob) => (knob.name === changed.name ? changed : knob)))}
            onDone={() => setEditing(null)}
          />
        </Paper>
      )}
    </Input.Wrapper>
  );
}

function TunedKnobForm({
  knob,
  description,
  onSet,
  onDone,
}: {
  knob: TunedKnob;
  description: KnobDescription | undefined;
  onSet: (knob: TunedKnob) => void;
  onDone: () => void;
}) {
  const problem = tunedProblem(knob, description);
  const { name, tier } = knob;
  return (
    <Stack gap="xs">
      <KnobHelp knob={description} name={name} />
      <Select
        label="Stage"
        data={TIERS.map((option) => ({ ...option }))}
        value={tier}
        onChange={(value) => value && onSet({ ...knob, tier: value as Tier })}
        allowDeselect={false}
        comboboxProps={{ withinPortal: false }}
      />
      {description?.tuned_as_range && (
        <SegmentedControl
          aria-label="Search"
          data={[
            { label: "Values", value: "values" },
            { label: "Range", value: "range" },
          ]}
          value={isRange(knob) ? "range" : "values"}
          onChange={(mode) =>
            onSet(
              mode === "range"
                ? { name, tier, min: undefined, max: undefined, default: knob.default }
                : { name, tier, values: [] },
            )
          }
        />
      )}
      {isRange(knob) ? (
        <Group grow>
          <NumberInput
            label="From"
            allowDecimal={false}
            value={knob.min ?? ""}
            onChange={(value) => onSet({ ...knob, min: typeof value === "number" ? value : undefined })}
          />
          <NumberInput
            label="To"
            allowDecimal={false}
            value={knob.max ?? ""}
            error={problem ?? undefined}
            onChange={(value) => onSet({ ...knob, max: typeof value === "number" ? value : undefined })}
          />
          <NumberInput
            label="First run"
            allowDecimal={false}
            value={typeof knob.default === "number" ? knob.default : ""}
            onChange={(value) => onSet({ ...knob, default: typeof value === "number" ? value : undefined })}
          />
        </Group>
      ) : (
        <ValuesInput
          knob={description}
          values={knob.values ?? []}
          error={problem}
          onSet={(values) => onSet({ ...knob, values })}
        />
      )}
      <KnobLimits knob={description} />
      <DoneButton onClick={onDone} />
    </Stack>
  );
}

/** The values a knob may take, with an input of its kind. */
function ValuesInput({
  knob,
  values,
  error,
  onSet,
}: {
  knob: KnobDescription | undefined;
  values: KnobValue[];
  error: string | null;
  onSet: (values: KnobValue[]) => void;
}) {
  const label = "Values";
  const kind = knob?.kind ?? "text";
  if (kind === "boolean" || kind === "switch")
    return (
      <Checkbox.Group
        label={label}
        error={error}
        value={values.map(String)}
        onChange={(picked) => onSet([false, true].filter((value) => picked.includes(String(value))))}
      >
        <Group mt={4}>
          <Checkbox value="true" label="On" />
          {kind === "boolean" && <Checkbox value="false" label="Off" />}
        </Group>
      </Checkbox.Group>
    );
  if (kind === "choice") {
    const choices = knob?.choices ?? [];
    return (
      <MultiSelect
        label={label}
        data={choices.map(String)}
        value={values.map(String)}
        error={error}
        onChange={(picked) => onSet(choices.filter((choice) => picked.includes(String(choice))) as KnobValue[])}
        comboboxProps={{ withinPortal: false }}
      />
    );
  }
  const isNumeric = NUMERIC_KINDS.has(kind);
  return (
    <TagsInput
      label={label}
      description="Enter each value, then Enter"
      spellCheck={false}
      data={isNumeric ? [] : (knob?.suggestions ?? [])}
      value={values.map(String)}
      error={error}
      onChange={(tags) =>
        onSet(tags.map((tag) => (isNumeric && tag.trim() !== "" && !Number.isNaN(Number(tag)) ? Number(tag) : tag)))
      }
      comboboxProps={{ withinPortal: false }}
    />
  );
}
