import { getJson, runId } from "./lib";

type Dataflow = {
  artifacts: Array<{
    artifactId?: string;
    producer?: { kind?: string; name?: string };
  }>;
};

/**
 * Asserts at least one reduced artifact exists (reducer in producer kind) when fixture includes reducers.
 */
export async function run(): Promise<void> {
  const id = runId();
  if (!id) {
    throw new Error("set RUN_ID for eval-reducer-required-dimensions");
  }
  const df = await getJson<Dataflow>(`/v1/runs/${encodeURIComponent(id)}/dataflow`);
  const hasReducer = (df.artifacts || []).some(
    (a) => a.producer?.kind === "reducer" || a.producer?.name?.toLowerCase().includes("reduc"),
  );
  if (!hasReducer) {
    throw new Error("no reducer-produced artifact in dataflow; fixture should emit reducer_* + artifact lines");
  }
  console.log("eval-reducer-required-dimensions: ok (reducer artifact present — full dimension check in producer)");
}
