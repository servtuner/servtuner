import {
  ActionIcon,
  Alert,
  Button,
  FileButton,
  Group,
  Loader,
  SegmentedControl,
  Stack,
  Table,
  Text,
  TextInput,
  Title,
} from "@mantine/core";
import { useState } from "react";
import { useDatasheet, usePriceBook, useSavePriceBook } from "../../api/hooks";
import type { PriceBook, PriceBookResource } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { Allowed } from "../auth/Allowed";
import {
  EMPTY_GPU,
  EMPTY_NODE,
  type GpuRow,
  type NodeRow,
  type PriceBookFields,
  datasheetLine,
  priceBookBody,
  priceBookError,
  priceBookFields,
  priceBookLine,
  usdPerHour,
} from "./priceBook";

/** What this install's Kubernetes machines cost: new runs and tuning sessions copy it
 * into their RunSpec, so each run keeps the prices it was made with. Hosts carry their own price. */
export function PriceBookSection() {
  const book = usePriceBook();
  const [isEditing, setIsEditing] = useState(false);
  return (
    <Stack gap="sm">
      <Title order={3}>Prices</Title>
      <Text c="dimmed" size="sm" maw={720}>
        What runs on Kubernetes cost: node types per hour, or GPUs per GPU-hour, or the AWS Price List. New runs and
        tuning sessions copy these into their spec, so changing them never reprices an earlier run. A host vLLM is
        priced by its host above.
      </Text>
      {book.isPending && <Loader size="sm" aria-label="Loading prices" />}
      {book.isError && <QueryError title="Could not load prices" error={book.error} />}
      {book.isSuccess &&
        (isEditing ? (
          <PriceBookForm existing={book.data} onDone={() => setIsEditing(false)} />
        ) : (
          <Stack gap="xs">
            <Text size="sm">{priceBookLine(book.data)}</Text>
            {book.data.source === "static" && <PriceTables book={book.data} />}
            <Allowed action="targets.write">
              <Group>
                <Button variant="default" onClick={() => setIsEditing(true)}>
                  Edit prices
                </Button>
              </Group>
            </Allowed>
            <DatasheetPanel book={book.data} />
          </Stack>
        ))}
    </Stack>
  );
}

/** The token-price datasheet the operator supplies: runs priced by the token use it. servtuner
 * ships no price data, so an install with no internet access uploads LiteLLM's model-price JSON here. */
function DatasheetPanel({ book }: { book: PriceBookResource }) {
  const { upload, remove } = useDatasheet();
  const error = upload.error ?? remove.error;
  return (
    <Stack gap={4} mt="sm">
      <Text size="sm" fw={500}>
        Token prices
      </Text>
      <Text size="sm">{datasheetLine(book)}</Text>
      <Text size="xs" c="dimmed" maw={720}>
        For routers in front of paid APIs. Download LiteLLM&apos;s model_prices_and_context_window.json on any connected
        machine and upload it, or give an internal URL under Edit prices. Each run keeps the copy it used.
      </Text>
      <Allowed action="targets.write">
        <Group>
          {book.datasheet?.source !== "url" && (
            <FileButton onChange={(file) => file && upload.mutate(file)} accept="application/json,.json">
              {(props) => (
                <Button {...props} variant="default" loading={upload.isPending}>
                  {book.datasheet ? "Replace datasheet" : "Upload datasheet"}
                </Button>
              )}
            </FileButton>
          )}
          {book.datasheet?.source === "upload" && (
            <Button variant="subtle" color="red" loading={remove.isPending} onClick={() => remove.mutate()}>
              Remove datasheet
            </Button>
          )}
        </Group>
      </Allowed>
      {error && (
        <Alert color="red" title="Datasheet not changed">
          {error.message}
        </Alert>
      )}
    </Stack>
  );
}

function PriceTables({ book }: { book: PriceBook }) {
  const nodes = Object.entries(book.nodes ?? {});
  const gpus = Object.entries(book.gpus ?? {});
  return (
    <Table verticalSpacing={4} maw={720}>
      <Table.Thead>
        <Table.Tr>
          <Table.Th>Node type or GPU model</Table.Th>
          <Table.Th>Price</Table.Th>
          <Table.Th>Allocatable</Table.Th>
        </Table.Tr>
      </Table.Thead>
      <Table.Tbody>
        {nodes.map(([key, price]) => (
          <Table.Tr key={`node-${key}`}>
            <Table.Td>{key}</Table.Td>
            <Table.Td>{usdPerHour(price.usd_per_hour)} per node</Table.Td>
            <Table.Td>
              {price.cores} cores · {price.memory_gib} GiB{price.gpus ? ` · ${price.gpus} GPUs` : ""}
            </Table.Td>
          </Table.Tr>
        ))}
        {gpus.map(([key, price]) => (
          <Table.Tr key={`gpu-${key}`}>
            <Table.Td>{key}</Table.Td>
            <Table.Td>{usdPerHour(price.usd_per_hour)} per GPU</Table.Td>
            <Table.Td />
          </Table.Tr>
        ))}
      </Table.Tbody>
    </Table>
  );
}

function PriceBookForm({ existing, onDone }: { existing: PriceBookResource; onDone: () => void }) {
  const save = useSavePriceBook();
  const [fields, setFields] = useState<PriceBookFields>(() => priceBookFields(existing));
  const set = (changes: Partial<PriceBookFields>) => setFields((current) => ({ ...current, ...changes }));
  const setNode = (index: number, changes: Partial<NodeRow>) =>
    set({ nodes: fields.nodes.map((row, at) => (at === index ? { ...row, ...changes } : row)) });
  const setGpu = (index: number, changes: Partial<GpuRow>) =>
    set({ gpus: fields.gpus.map((row, at) => (at === index ? { ...row, ...changes } : row)) });
  const problem = priceBookError(fields);
  return (
    <Stack gap="sm" maw={900}>
      <SegmentedControl
        aria-label="Price source"
        w="fit-content"
        value={fields.source}
        onChange={(source) => set({ source: source as PriceBookFields["source"] })}
        data={[
          { value: "none", label: "None" },
          { value: "static", label: "Price table" },
          // An offline install cannot reach the AWS Price List; the api refuses it too.
          ...(existing.offline && fields.source !== "aws" ? [] : [{ value: "aws", label: "AWS Price List" }]),
        ]}
      />
      {fields.source === "aws" && (
        <TextInput
          label="AWS region"
          description="Where the nodes run; servtuner asks the Price List for the node type each run lands on"
          placeholder="us-east-1"
          spellCheck={false}
          w={320}
          value={fields.awsRegion}
          onChange={(event) => set({ awsRegion: event.currentTarget.value })}
        />
      )}
      {fields.source === "static" && (
        <>
          <Text size="sm" fw={500}>
            Node types
          </Text>
          <Text size="xs" c="dimmed" mt={-8}>
            Keyed by node.kubernetes.io/instance-type; a pod pays its share of the node's cores, memory and GPUs.
          </Text>
          {fields.nodes.map((row, index) => (
            <Group key={index} gap="xs" align="end" wrap="nowrap">
              <TextInput
                label={index === 0 ? "Instance type" : undefined}
                aria-label="Instance type"
                spellCheck={false}
                value={row.key}
                onChange={(event) => setNode(index, { key: event.currentTarget.value })}
              />
              <TextInput
                label={index === 0 ? "USD per hour" : undefined}
                aria-label="USD per hour"
                inputMode="decimal"
                w={120}
                value={row.usdPerHour}
                onChange={(event) => setNode(index, { usdPerHour: event.currentTarget.value })}
              />
              <TextInput
                label={index === 0 ? "Cores" : undefined}
                aria-label="Cores"
                inputMode="decimal"
                w={90}
                value={row.cores}
                onChange={(event) => setNode(index, { cores: event.currentTarget.value })}
              />
              <TextInput
                label={index === 0 ? "Memory (GiB)" : undefined}
                aria-label="Memory (GiB)"
                inputMode="decimal"
                w={110}
                value={row.memoryGib}
                onChange={(event) => setNode(index, { memoryGib: event.currentTarget.value })}
              />
              <TextInput
                label={index === 0 ? "GPUs" : undefined}
                aria-label="GPUs"
                inputMode="numeric"
                w={80}
                value={row.gpus}
                onChange={(event) => setNode(index, { gpus: event.currentTarget.value })}
              />
              <ActionIcon
                variant="subtle"
                color="gray"
                aria-label={`Remove node type ${row.key}`}
                onClick={() => set({ nodes: fields.nodes.filter((_, at) => at !== index) })}
                mb={4}
              >
                ×
              </ActionIcon>
            </Group>
          ))}
          <Group>
            <Button variant="subtle" size="xs" onClick={() => set({ nodes: [...fields.nodes, EMPTY_NODE] })}>
              Add a node type
            </Button>
          </Group>
          <Text size="sm" fw={500}>
            GPU models
          </Text>
          <Text size="xs" c="dimmed" mt={-8}>
            Per GPU-hour, keyed by the model a run's snapshot records (e.g. NVIDIA H200).
          </Text>
          {fields.gpus.map((row, index) => (
            <Group key={index} gap="xs" align="end" wrap="nowrap">
              <TextInput
                label={index === 0 ? "GPU model" : undefined}
                aria-label="GPU model"
                spellCheck={false}
                w={260}
                value={row.key}
                onChange={(event) => setGpu(index, { key: event.currentTarget.value })}
              />
              <TextInput
                label={index === 0 ? "USD per GPU-hour" : undefined}
                aria-label="USD per GPU-hour"
                inputMode="decimal"
                w={140}
                value={row.usdPerHour}
                onChange={(event) => setGpu(index, { usdPerHour: event.currentTarget.value })}
              />
              <ActionIcon
                variant="subtle"
                color="gray"
                aria-label={`Remove GPU model ${row.key}`}
                onClick={() => set({ gpus: fields.gpus.filter((_, at) => at !== index) })}
                mb={4}
              >
                ×
              </ActionIcon>
            </Group>
          ))}
          <Group>
            <Button variant="subtle" size="xs" onClick={() => set({ gpus: [...fields.gpus, EMPTY_GPU] })}>
              Add a GPU model
            </Button>
          </Group>
        </>
      )}
      {fields.source !== "none" && (
        <TextInput
          label="Discount multiplier"
          description="1 pays list price; 0.7 pays 70% (Spot, reserved instances, a Savings Plan)"
          inputMode="decimal"
          w={320}
          value={fields.discount}
          onChange={(event) => set({ discount: event.currentTarget.value })}
        />
      )}
      {existing.datasheet?.source !== "upload" && (
        <TextInput
          label="Token-price datasheet URL"
          description="Optional: an internal copy of LiteLLM's model-price JSON that servtuner can reach, instead of an upload"
          placeholder="https://prices.example.internal/model_prices.json"
          spellCheck={false}
          maw={560}
          value={fields.datasheetUrl}
          onChange={(event) => set({ datasheetUrl: event.currentTarget.value })}
        />
      )}
      {save.error && (
        <Alert color="red" title="Not saved">
          {save.error.message}
        </Alert>
      )}
      <Group>
        <Button
          loading={save.isPending}
          disabled={problem !== null}
          title={problem ?? undefined}
          onClick={() => save.mutate(priceBookBody(fields), { onSuccess: onDone })}
        >
          Save prices
        </Button>
        <Button variant="default" onClick={onDone}>
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
