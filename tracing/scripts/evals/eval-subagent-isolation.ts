import { getJson, runId } from "./lib";

type EventRow = {
  event_type: string;
  subagent_id?: string;
  payload_json?: Record<string, unknown>;
};

/**
 * Sub-agent isolation (collector-side heuristic).
 * If EVAL_SUBAGENT_ID is set, we require that id to appear in telemetry and that a probe
 * marker does not appear in any payload string.
 */
export async function run(): Promise<void> {
  const id = runId();
  if (!id) {
    throw new Error("set RUN_ID for eval-subagent-isolation");
  }
  const sid = process.env.EVAL_SUBAGENT_ID || "";
  if (!sid) {
    console.log("eval-subagent-isolation: skipped (set EVAL_SUBAGENT_ID for strict check)");
    return;
  }
  const ev = await getJson<EventRow[]>(`/v1/traces/${encodeURIComponent(id)}/events`);
  const combined = ev.map((e) => JSON.stringify(e)).join("\n");
  if (!combined.includes(sid)) {
    throw new Error(
      `expected events to reference subagent_id ${sid}; set fixture or expand TraceEvents to expose subagent_id`,
    );
  }
  const probe = "subagent-leak-probe-xyz";
  if (combined.includes(probe)) {
    throw new Error("detected isolation probe marker in trace payloads");
  }
  console.log("eval-subagent-isolation: ok (heuristic; full messages[] check lives in pyyol-api)");
}
