import {
  Anchor,
  Autocomplete,
  Button,
  CloseButton,
  Code,
  Group,
  Input,
  NumberInput,
  Paper,
  SegmentedControl,
  Select,
  Stack,
  Text,
  UnstyledButton,
} from "@mantine/core";
import { useState } from "react";
import type { KnobDescription } from "../../api/types";
import {
  describeLimits,
  displayValue,
  initialValue,
  type KnobEntry,
  type KnobValue,
  matchesSearch,
  NUMERIC_KINDS,
  problemOf,
} from "./knobValues";

/**
 * vLLM knobs as chips ("Max sequences 128"): click one to edit it below, with an input of its type,
 * inside the limits vLLM's source sets; "Add a knob" searches the catalog by name or flag. Labels,
 * descriptions and limits all come from GET /knobs, never from this file.
 */
export function KnobEditor({
  label,
  description,
  entries,
  catalog,
  exclude,
  onChange,
}: {
  label: string;
  description: string;
  entries: KnobEntry[];
  catalog: Map<string, KnobDescription>;
  /** Knobs the picker leaves out besides these entries (a space's tuned knobs). */
  exclude?: ReadonlySet<string>;
  onChange: (entries: KnobEntry[]) => void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const set = (name: string, value: KnobValue | null) =>
    onChange(entries.map((entry) => (entry.name === name ? { name, value } : entry)));
  const remove = (name: string) => onChange(entries.filter((entry) => entry.name !== name));
  const add = (name: string) => {
    const knob = catalog.get(name);
    if (!knob) return;
    onChange([...entries, { name, value: initialValue(knob) }]);
    setEditing(name);
  };
  const open = entries.find((entry) => entry.name === editing);
  return (
    <Input.Wrapper label={label} description={description}>
      <Group gap="xs" mt={6} role="list" aria-label={label}>
        {entries.map((entry) => (
          <KnobChip
            key={entry.name}
            knob={catalog.get(entry.name)}
            name={entry.name}
            value={displayValue(entry, catalog.get(entry.name))}
            problem={problemOf(entry, catalog.get(entry.name))}
            isEditing={editing === entry.name}
            onEdit={() => setEditing(editing === entry.name ? null : entry.name)}
            onRemove={() => {
              if (editing === entry.name) setEditing(null);
              remove(entry.name);
            }}
          />
        ))}
        <KnobPicker
          catalog={catalog}
          taken={new Set([...entries.map((entry) => entry.name), ...(exclude ?? [])])}
          onPick={add}
        />
      </Group>
      {open && (
        <Paper withBorder p="sm" mt="xs" maw={480}>
          <KnobForm
            key={open.name}
            entry={open}
            knob={catalog.get(open.name)}
            problem={problemOf(open, catalog.get(open.name))}
            onSet={(value) => set(open.name, value)}
            onDone={() => setEditing(null)}
          />
        </Paper>
      )}
    </Input.Wrapper>
  );
}

/** One knob as a rounded chip: its label and value, red while it has a problem, the flag on hover. */
export function KnobChip({
  knob,
  name,
  value,
  problem,
  isEditing,
  onEdit,
  onRemove,
}: {
  knob: KnobDescription | undefined;
  name: string;
  value: string;
  problem: string | null;
  isEditing: boolean;
  onEdit: () => void;
  onRemove: () => void;
}) {
  const label = knob?.label ?? name;
  return (
    <Paper
      role="listitem"
      withBorder
      radius="xl"
      pl="sm"
      pr={4}
      py={2}
      bg={problem ? "var(--mantine-color-red-light)" : "var(--mantine-color-default-hover)"}
      bd={isEditing ? "1px solid var(--mantine-primary-color-filled)" : undefined}
    >
      <Group gap={4} wrap="nowrap">
        <UnstyledButton onClick={onEdit} title={knob?.flag || name} aria-expanded={isEditing}>
          <Text size="sm" span>
            {label}{" "}
            <Text span fw={700} inherit c={problem ? "red" : undefined}>
              {value}
            </Text>
          </Text>
        </UnstyledButton>
        <CloseButton size="sm" aria-label={`Remove ${label}`} onClick={onRemove} />
      </Group>
    </Paper>
  );
}

/** A knob's name, flag and vLLM's description (its first paragraph, the rest on request). */
export function KnobHelp({ knob, name }: { knob: KnobDescription | undefined; name: string }) {
  const [isFull, setIsFull] = useState(false);
  const help = knob?.help ?? "";
  const [summary, ...rest] = help.split(/\n\s*\n/);
  return (
    <>
      <Group gap="xs" justify="space-between" wrap="nowrap">
        <Text fw={600}>{knob?.label ?? name}</Text>
        <Code>{knob?.flag || name}</Code>
      </Group>
      {summary && (
        <Text size="xs" c="dimmed" style={{ whiteSpace: "pre-line" }}>
          {isFull ? help : summary}
          {rest.length > 0 && (
            <>
              {" "}
              <Anchor component="button" type="button" size="xs" onClick={() => setIsFull(!isFull)}>
                {isFull ? "Less" : "More"}
              </Anchor>
            </>
          )}
        </Text>
      )}
    </>
  );
}

/** What vLLM takes and its default, under a knob's input. */
export function KnobLimits({ knob }: { knob: KnobDescription | undefined }) {
  const limits = knob && describeLimits(knob);
  return (
    <Text size="xs" c="dimmed">
      {limits ? `vLLM takes ${limits}. ` : ""}
      Default: {knob?.default === undefined || knob.default === null ? "vLLM decides" : String(knob.default)}
    </Text>
  );
}

function KnobForm({
  entry,
  knob,
  problem,
  onSet,
  onDone,
}: {
  entry: KnobEntry;
  knob: KnobDescription | undefined;
  problem: string | null;
  onSet: (value: KnobValue | null) => void;
  onDone: () => void;
}) {
  return (
    <Stack gap="xs">
      <KnobHelp knob={knob} name={entry.name} />
      <KnobInput entry={entry} knob={knob} problem={problem} onSet={onSet} />
      <KnobLimits knob={knob} />
      <DoneButton onClick={onDone} />
    </Stack>
  );
}

export function DoneButton({ onClick }: { onClick: () => void }) {
  return (
    <Group justify="flex-end">
      <Button size="xs" variant="default" onClick={onClick}>
        Done
      </Button>
    </Group>
  );
}

function KnobInput({
  entry,
  knob,
  problem,
  onSet,
}: {
  entry: KnobEntry;
  knob: KnobDescription | undefined;
  problem: string | null;
  onSet: (value: KnobValue | null) => void;
}) {
  const label = "Value";
  const kind = knob?.kind ?? "text";
  const error = entry.value === null ? undefined : problem;
  if (NUMERIC_KINDS.has(kind))
    return (
      <NumberInput
        label={label}
        allowDecimal={kind === "number"}
        value={typeof entry.value === "number" ? entry.value : ""}
        error={error}
        onChange={(value) => onSet(typeof value === "number" ? value : value === "" ? null : Number(value))}
      />
    );
  if (kind === "boolean")
    return (
      <SegmentedControl
        aria-label={label}
        data={[
          { label: "On", value: "true" },
          { label: "Off", value: "false" },
        ]}
        value={entry.value === false ? "false" : "true"}
        onChange={(value) => onSet(value === "true")}
      />
    );
  if (kind === "switch") return <Text size="sm">On: vLLM has no flag that turns it off.</Text>;
  if (kind === "choice")
    return (
      <Select
        label={label}
        data={(knob?.choices ?? []).map(String)}
        value={entry.value === null ? null : String(entry.value)}
        error={error}
        onChange={(value) => onSet(value)}
        comboboxProps={{ withinPortal: false }}
      />
    );
  return (
    <Autocomplete
      label={label}
      spellCheck={false}
      data={knob?.suggestions ?? []}
      value={typeof entry.value === "string" ? entry.value : entry.value === null ? "" : String(entry.value)}
      error={error}
      onChange={(value) => onSet(value === "" ? null : value)}
      comboboxProps={{ withinPortal: false }}
    />
  );
}

/** A searchable list of the catalog's knobs not yet set, servtuner's labelled ones first; picking one
 * adds it and opens it. Every vLLM flag with one value is a knob. */
export function KnobPicker({
  catalog,
  taken,
  onPick,
}: {
  catalog: Map<string, KnobDescription>;
  taken: ReadonlySet<string>;
  onPick: (name: string) => void;
}) {
  const knobs = [...catalog.values()]
    .filter((knob) => !taken.has(knob.name))
    .sort((a, b) => Number(b.built_in) - Number(a.built_in));
  const [search, setSearch] = useState("");
  return (
    <Select
      aria-label="Add a knob"
      placeholder={catalog.size > 0 ? "Add a knob" : "Loading knobs…"}
      disabled={catalog.size === 0}
      size="xs"
      w={220}
      searchable
      spellCheck={false}
      value={null}
      searchValue={search}
      onSearchChange={setSearch}
      nothingFoundMessage="No knob by that name or flag"
      comboboxProps={{ withinPortal: false }}
      data={knobs.map((knob) => ({ value: knob.name, label: knob.label }))}
      filter={({ options, search }) =>
        options.filter((option) => {
          const knob = "value" in option ? catalog.get(option.value) : undefined;
          return knob !== undefined && matchesSearch(knob, search);
        })
      }
      renderOption={({ option }) => (
        <Stack gap={0}>
          <Text size="sm">{option.label}</Text>
          <Text size="xs" c="dimmed" ff="monospace">
            {catalog.get(option.value)?.flag}
          </Text>
        </Stack>
      )}
      onChange={(name) => {
        setSearch("");
        if (name) onPick(name);
      }}
    />
  );
}
