import { Alert, Button, Group, SegmentedControl, SimpleGrid, Stack, Text, TextInput } from "@mantine/core";
import { useState } from "react";
import type { Host, HostCreate } from "../../api/types";
import { FIELD_GRID_STYLE } from "../../components/layout";
import { type HostFields, hostBody, hostFields, hostFormError } from "./summary";

/** Create or edit a host: its name, what the machine is, and what it costs per hour. */
export function HostForm({
  existing,
  submitLabel,
  isPending,
  error,
  onSubmit,
  onCancel,
}: {
  existing?: Host;
  submitLabel: string;
  isPending: boolean;
  error: string | undefined;
  onSubmit: (body: HostCreate) => void;
  onCancel: () => void;
}) {
  const [fields, setFields] = useState<HostFields>(() => hostFields(existing));
  const field = (key: keyof HostFields) => ({
    value: fields[key],
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => {
      const value = event.currentTarget.value;
      setFields((current) => ({ ...current, [key]: value }));
    },
  });
  const problem = hostFormError(fields);
  return (
    <Stack gap="sm" maw={900}>
      <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE}>
        <TextInput label="Name" description="Lower case, e.g. gpu-box" required spellCheck={false} {...field("name")} />
        <TextInput label="Note" description="Optional: where it is, who owns it" {...field("note")} />
      </SimpleGrid>
      <Text size="sm" fw={500}>
        Hardware
      </Text>
      <Text size="xs" c="dimmed" mt={-8}>
        Or leave it and read it from the machine's launcher once a managed vLLM runs on it.
      </Text>
      <SimpleGrid cols={{ base: 1, md: 3 }} style={FIELD_GRID_STYLE}>
        <TextInput label="GPU model" placeholder="NVIDIA GeForce RTX 3070" {...field("gpuModel")} />
        <TextInput label="GPUs" inputMode="numeric" {...field("gpuCount")} />
        <TextInput label="GPU memory (GiB)" inputMode="decimal" {...field("gpuMemoryGib")} />
        <TextInput label="CPU cores" inputMode="numeric" {...field("cpuCores")} />
        <TextInput label="Memory (GiB)" inputMode="decimal" {...field("memoryGib")} />
      </SimpleGrid>
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          Price
        </Text>
        <SegmentedControl
          aria-label="Price"
          w="fit-content"
          value={fields.priceKind}
          onChange={(kind) => setFields((current) => ({ ...current, priceKind: kind as HostFields["priceKind"] }))}
          data={[
            { value: "none", label: "None" },
            { value: "given", label: "Per hour" },
            { value: "owned", label: "Owned hardware" },
          ]}
        />
      </Stack>
      {fields.priceKind === "given" && (
        <TextInput
          label="USD per hour"
          description="The whole machine: a rental's rate, or your own figure"
          inputMode="decimal"
          w={240}
          {...field("usdPerHour")}
        />
      )}
      {fields.priceKind === "owned" && (
        <SimpleGrid cols={{ base: 1, md: 4 }} style={FIELD_GRID_STYLE}>
          <TextInput label="Purchase (USD)" inputMode="decimal" {...field("purchaseUsd")} />
          <TextInput
            label="Spread over (hours)"
            description="8760 is a year"
            inputMode="decimal"
            {...field("amortizationHours")}
          />
          <TextInput label="Power (W)" description="The machine under load" inputMode="decimal" {...field("watts")} />
          <TextInput label="Electricity (USD/kWh)" inputMode="decimal" {...field("usdPerKwh")} />
        </SimpleGrid>
      )}
      {error && (
        <Alert color="red" title="Not saved">
          {error}
        </Alert>
      )}
      <Group>
        <Button
          loading={isPending}
          disabled={problem !== null}
          title={problem ?? undefined}
          onClick={() => onSubmit(hostBody(fields, existing))}
        >
          {submitLabel}
        </Button>
        <Button variant="default" onClick={onCancel}>
          Cancel
        </Button>
        {problem && (
          <Text size="xs" c="dimmed">
            {problem}
          </Text>
        )}
      </Group>
    </Stack>
  );
}
