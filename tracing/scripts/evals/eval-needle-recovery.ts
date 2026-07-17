import { getJson, runId } from "./lib";

type Events = Array<{
  event_type: string;
  span_id: string;
  payload_json?: Record<string, unknown>;
}>;

/**
 * Deep-Agents style needle: expects a read_artifact / artifact_read (or tool) after synthesis
 * when the model must re-fetch. Collector-only stub: pass if any artifact_read exists or SKIP if unset.
 */
export async function run(): Promise<void> {
  const id = runId();
  if (!id) {
    throw new Error("set RUN_ID for eval-needle-recovery");
  }
  if (process.env.EVAL_SKIP_NEEDLE === "1") {
    console.log("eval-needle-recovery: skipped (EVAL_SKIP_NEEDLE=1)");
    return;
  }
  const ev = await getJson<Events>(`/v1/traces/${encodeURIComponent(id)}/events`);
  const ok = ev.some(
    (e) => e.event_type === "artifact_read" || (e.payload_json as { tool_name?: string })?.tool_name === "read_artifact",
  );
  if (!ok) {
    throw new Error(
      "expected artifact_read or read_artifact in trace events (needle recovery pattern); re-run with fixture that emits these",
    );
  }
  console.log("eval-needle-recovery: ok");
}
