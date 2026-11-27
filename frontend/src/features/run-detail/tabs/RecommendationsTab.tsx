import { Alert, Anchor, Badge, Group, Paper, SimpleGrid, Stack, Text } from "@mantine/core";
import { useApiClient, useRunDocument, useRunFiles } from "../../../api/hooks";
import type { Recommendations, RunStatus } from "../../../api/types";
import { formatBytes, formatNumber } from "../../../components/format";
import { QueueTermBadge, QueueTerms } from "../../../components/QueueTerms";
import { mentionsQueueTerms } from "../../../components/queueTermDefinitions";
import { canFollowUp } from "../../followup/canFollowUp";
import { FollowupButton } from "../../followup/FollowupButton";
import { WithDocument } from "./NotAnalysed";

type Recommendation = Recommendations["recommendations"][number];
type DerivationNode = NonNullable<Recommendations["derivation"]>["nodes"][number];

const PATCHES = [
  { path: "patches/resources.yaml", label: "Resources patch" },
  { path: "patches/bifrost.yaml", label: "Bifrost config patch" },
];

export function RecommendationsTab({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const recommendations = useRunDocument("getRecommendations", runId, status);
  return (
    <WithDocument query={recommendations} what="recommendations">
      {(document) => <RecommendationList runId={runId} status={status} document={document} />}
    </WithDocument>
  );
}

function RecommendationList({
  runId,
  status,
  document,
}: {
  runId: string;
  status: RunStatus | undefined;
  document: Recommendations;
}) {
  const nodes = new Map((document.derivation?.nodes ?? []).map((node) => [node.id, node]));
  const point = document.operating_point;
  // Caveats about the operating point are said once here, not on every card.
  const shared = (point?.caveats ?? []).map(String);
  return (
    <Stack gap="lg">
      <Group justify="space-between" align="start">
        <Text size="sm" maw={640}>
          {point?.rps != null
            ? `Sized for ${formatNumber(point.rps)} RPS per replica at stage ${point.stage_index}, the ${point.mode === "slo_max" ? "highest stage that met the SLOs" : "target load"}.`
            : "No stage was valid and met the SLOs, so nothing is sized; the advice below still applies."}
        </Text>
        <PatchLinks runId={runId} status={status} />
      </Group>
      {shared.length > 0 && (
        <Alert variant="light" color="orange" title="About the load these sizes are for">
          <Stack gap={4}>
            {shared.map((caveat) => (
              <Text key={caveat} size="sm">
                {caveat}
              </Text>
            ))}
          </Stack>
        </Alert>
      )}
      {mentionsQueueTerms(
        document.recommendations.flatMap((r) => [...r.caveats.map(String), ...r.evidence.map((e) => e.text)]),
      ) && <QueueTerms />}
      {document.recommendations.length === 0 ? (
        <Text c="dimmed">No recommendations: nothing in this run calls for a change.</Text>
      ) : (
        <SimpleGrid cols={{ base: 1, md: 2 }} spacing="md">
          {document.recommendations.map((recommendation) => (
            <RecommendationCard
              key={recommendation.id}
              recommendation={recommendation}
              node={recommendation.derivation_node ? nodes.get(recommendation.derivation_node) : undefined}
              hiddenCaveats={shared}
              runId={runId}
            />
          ))}
        </SimpleGrid>
      )}
    </Stack>
  );
}

function PatchLinks({ runId, status }: { runId: string; status: RunStatus | undefined }) {
  const client = useApiClient();
  const files = useRunFiles(runId, status);
  const present = new Set((files.data ?? []).map((file) => file.path));
  const patches = PATCHES.filter((patch) => present.has(patch.path));
  if (patches.length === 0) return null;
  return (
    <Group gap="md">
      {patches.map((patch) => (
        <Anchor key={patch.path} href={client.fileUrl(runId, patch.path)} download size="sm">
          Download {patch.label.toLowerCase()}
        </Anchor>
      ))}
    </Group>
  );
}

function value(raw: Recommendation["recommended"] | undefined, unit: string | null | undefined): string {
  if (raw == null) return "–";
  const number = typeof raw === "number" ? raw : /^\d+(\.\d+)?$/.test(String(raw)) ? Number(raw) : null;
  if (unit === "bytes" && number != null) return formatBytes(number);
  if (number == null) return String(raw);
  return unit ? `${formatNumber(number)} ${unit}` : formatNumber(number);
}

const CONFIDENCE_LABELS = {
  high: "High confidence",
  medium: "Medium confidence",
  low: "Low confidence",
} as const;

function RecommendationCard({
  recommendation,
  node,
  hiddenCaveats,
  runId,
}: {
  recommendation: Recommendation;
  node: DerivationNode | undefined;
  hiddenCaveats: string[];
  runId: string;
}) {
  const caveats = recommendation.caveats.map(String).filter((caveat) => !hiddenCaveats.includes(caveat));
  const { target, subject } = recommendation;
  const where = [target.container, target.namespace].filter(Boolean).join(" in ");
  const queueSymbol = node?.id === "queue.C" ? "C" : node?.id === "queue.B" ? "B" : undefined;
  return (
    <Paper withBorder p="md" radius="sm">
      <Stack gap="sm">
        <Group justify="space-between" align="start" wrap="nowrap">
          <div>
            <Group gap="xs" wrap="nowrap">
              <Text fw={600}>{subject.name}</Text>
              {queueSymbol && <QueueTermBadge symbol={queueSymbol} />}
            </Group>
            <Text size="xs" c="dimmed">
              {target.target_type}
              {where && `, ${where}`}
            </Text>
          </div>
          <Group gap={6} wrap="nowrap">
            {recommendation.is_urgent && (
              <Badge color="red" variant="filled" size="sm">
                Urgent
              </Badge>
            )}
            <Badge variant="light" color={recommendation.tier === "empirical" ? "teal" : "gray"} size="sm">
              {recommendation.tier === "empirical" ? "Measured across runs" : "From this run"}
            </Badge>
            <Badge
              variant="outline"
              color={{ high: "teal", medium: "yellow", low: "gray" }[recommendation.confidence]}
              size="sm"
            >
              {CONFIDENCE_LABELS[recommendation.confidence]}
            </Badge>
          </Group>
        </Group>
        <Group gap="xs" align="baseline" style={{ fontVariantNumeric: "tabular-nums" }}>
          {recommendation.current != null && (
            <>
              <Text size="lg" c="dimmed" td="line-through">
                {value(recommendation.current, recommendation.unit === "bytes" ? "bytes" : null)}
              </Text>
              <Text c="dimmed" aria-hidden>
                to
              </Text>
            </>
          )}
          <Text size="xl" fw={700}>
            {value(recommendation.recommended, recommendation.unit)}
          </Text>
          {(recommendation.min != null || recommendation.max != null) && (
            <Text size="xs" c="dimmed">
              range {value(recommendation.min, recommendation.unit)} to {value(recommendation.max, recommendation.unit)}
            </Text>
          )}
        </Group>
        {node?.rendered && (
          <Text size="xs" ff="monospace" c="dimmed">
            {node.rendered}
          </Text>
        )}
        {recommendation.evidence.length > 0 && (
          <Stack gap={2}>
            {recommendation.evidence.map((item, index) => (
              <Text key={index} size="sm">
                {item.text}
                {item.stage_index != null && (
                  <Text span size="xs" c="dimmed">
                    {" "}
                    (stage {item.stage_index})
                  </Text>
                )}
              </Text>
            ))}
          </Stack>
        )}
        {canFollowUp(recommendation) && (
          <Group justify="flex-end">
            <FollowupButton runId={runId} recommendation={recommendation} />
          </Group>
        )}
        {caveats.length > 0 && (
          <Stack gap={2}>
            {caveats.map((caveat) => (
              <Text key={caveat} size="xs" c="orange.8">
                {caveat}
              </Text>
            ))}
          </Stack>
        )}
      </Stack>
    </Paper>
  );
}
