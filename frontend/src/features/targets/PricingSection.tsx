import { SegmentedControl, Select, SimpleGrid, Stack, Text, TextInput, Title } from "@mantine/core";
import { useHosts, usePriceBook } from "../../api/hooks";
import { FIELD_GRID_STYLE } from "../../components/layout";
import type { PricingFields } from "./pricing";

/** How a target servtuner does not manage is paid for: by the hour, by the token, or both. */
export function PricingSection({
  fields,
  onChange,
}: {
  fields: PricingFields;
  onChange: (changes: Partial<PricingFields>) => void;
}) {
  const hosts = useHosts();
  const book = usePriceBook();
  const text = (key: keyof PricingFields) => ({
    value: fields[key],
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => onChange({ [key]: event.currentTarget.value }),
  });
  const nodeTypes = Object.keys(book.data?.nodes ?? {});
  return (
    <Stack gap="sm">
      <Title order={5}>How it is paid for</Title>
      <Text size="sm" c="dimmed">
        Runs against this target carry these prices, so their cost per request is known. A target in this cluster is
        already priced by its node through the price book.
      </Text>
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          By the hour
        </Text>
        <SegmentedControl
          aria-label="Hourly price"
          w="fit-content"
          value={fields.hourly}
          onChange={(value) => onChange({ hourly: value as PricingFields["hourly"] })}
          data={[
            { value: "none", label: "No" },
            { value: "host", label: "A host" },
            { value: "node_type", label: "A node type" },
          ]}
        />
      </Stack>
      {fields.hourly === "host" && (
        <Select
          label="Host"
          description="Its hardware and price per hour, from the Hosts on Managed Servers"
          placeholder={hosts.data?.length === 0 ? "No hosts yet" : "Pick a host"}
          data={(hosts.data ?? []).map((host) => ({ value: host.id, label: host.name }))}
          value={fields.hostId || null}
          onChange={(value) => onChange({ hostId: value ?? "" })}
          w={320}
        />
      )}
      {fields.hourly === "node_type" && (
        <SimpleGrid cols={{ base: 1, md: 3 }} style={FIELD_GRID_STYLE}>
          <Select
            label="Node type"
            description="From the price book"
            placeholder={nodeTypes.length === 0 ? "The price book lists none" : "Pick a node type"}
            data={nodeTypes}
            value={fields.nodeType || null}
            onChange={(value) => onChange({ nodeType: value ?? "" })}
          />
          <TextInput label="GPUs it holds" inputMode="numeric" {...text("gpuCount")} />
          <TextInput
            label="GPU model"
            description="Optional: uses the book's price for this model"
            spellCheck={false}
            {...text("gpuModel")}
          />
        </SimpleGrid>
      )}
      <Stack gap={4}>
        <Text size="sm" fw={500}>
          By the token
        </Text>
        <SegmentedControl
          aria-label="Token price"
          w="fit-content"
          value={fields.token}
          onChange={(value) => onChange({ token: value as PricingFields["token"] })}
          data={[
            { value: "none", label: "No" },
            { value: "datasheet", label: "Datasheet" },
            { value: "manual", label: "Given prices" },
          ]}
        />
      </Stack>
      {fields.token === "datasheet" && (
        <TextInput
          label="Datasheet URL"
          description="Optional: LiteLLM's model-price JSON; by default Bifrost's own or getbifrost.ai's"
          spellCheck={false}
          maw={560}
          {...text("url")}
        />
      )}
      {fields.token === "manual" && (
        <SimpleGrid cols={{ base: 1, md: 2 }} style={FIELD_GRID_STYLE} maw={560}>
          <TextInput label="USD per 1M input tokens" inputMode="decimal" {...text("inputPer1m")} />
          <TextInput label="USD per 1M output tokens" inputMode="decimal" {...text("outputPer1m")} />
        </SimpleGrid>
      )}
    </Stack>
  );
}
