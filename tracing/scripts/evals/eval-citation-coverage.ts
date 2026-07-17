import { getJson, runId } from "./lib";

type Dataflow = {
  citations: Array<{
    citationId?: string;
    claim?: string;
    evidenceIds?: string[];
  }>;
  edges: Array<{ from: string; to: string; kind: string }>;
};

/**
 * Placeholder: full check parses synthesis and compares to agent_citations in Mongo.
 * Here we only assert the collector has citation nodes when citation_completed was emitted
 * (fixture runs should have citations.length > 0 or edges to citation:*).
 */
export async function run(): Promise<void> {
  const id = runId();
  if (!id) {
    throw new Error("set RUN_ID for eval-citation-coverage");
  }
  const df = await getJson<Dataflow>(`/v1/runs/${encodeURIComponent(id)}/dataflow`);
  const n = (df.citations && df.citations.length) || 0;
  const hasCitEdge = (df.edges || []).some(
    (e) => e.kind === "produced" && (e.to.startsWith("citation:") || e.from.startsWith("citation:")),
  );
  if (n === 0 && !hasCitEdge) {
    throw new Error(
      "no citations in dataflow (expect citation_completed to populate citations or edges in fixture run)",
    );
  }
  console.log("eval-citation-coverage: ok (citations or citation edges present)");
}
