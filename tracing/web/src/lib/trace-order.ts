import type { SpanNode } from "@/lib/pyyol-lens-api";

/** Pre-order traversal index (matches typical “what ran first” tree order; aligns with backend `started_at` sort). */
export function preorderExecutionOrder(tree: SpanNode[]): Map<string, number> {
  const map = new Map<string, number>();
  let seq = 1;
  const walk = (nodes: SpanNode[]) => {
    for (const node of nodes) {
      map.set(node.span_id, seq);
      seq += 1;
      if (node.children?.length) walk(node.children);
    }
  };
  walk(tree);
  return map;
}

export function parseTraceStartMs(startedAt: string): number | null {
  const t = Date.parse(startedAt);
  return Number.isFinite(t) ? t : null;
}

export function formatOffsetFromStart(traceStartMs: number | null, spanStartedAt: string): string | null {
  if (traceStartMs == null) return null;
  const s = Date.parse(spanStartedAt);
  if (!Number.isFinite(s)) return null;
  const delta = Math.max(0, s - traceStartMs);
  if (delta < 1000) return `+${Math.round(delta)}ms`;
  return `+${(delta / 1000).toFixed(2)}s`;
}

/** Human-readable role for reducer / read / write / tool / LLM style spans. */
export function spanRoleLabel(span: SpanNode): string | undefined {
  const tk = (span.task_kind || "").toLowerCase().trim();
  if (tk.includes("reduc")) return "Reducer";
  if (tk.includes("read") || tk.includes("retriev")) return "Read";
  if (tk.includes("write") || tk.includes("persist") || tk.includes("artifact_out")) return "Write";
  if (tk) return span.task_kind;

  const st = (span.span_type || "").toLowerCase();
  if (st === "tool" || st === "function" || st === "tool_call") return "Tool";
  if (st === "llm" || st === "model_call" || st.includes("model")) return "LLM";
  if (st === "retrieval") return "Read";
  if (st === "embedding") return "Embed";
  if (st === "agent_call") return "Agent";
  if (st === "match") return "Match";
  if (st === "decision") return "Decision";
  if (st === "trace_completion") return "Done";
  if (st === "chat") return "Chat";
  if (st === "lifecycle") return "Lifecycle";
  if (st === "log") return "Log";
  if (st === "agent_notify") return "Notify";
  if (st === "benchmark") return "Benchmark";
  if (st === "domain_event") return "Event";
  if (st === "agent_turn") return "Turn";
  return undefined;
}

export function sortEventsByTime<T extends { event_time: string }>(rows: T[]): T[] {
  return [...rows].sort((a, b) => Date.parse(a.event_time) - Date.parse(b.event_time));
}

/** Span ids with an unmatched *_started telemetry event while the trace is still running. */
export function activeSpanIdsFromEvents(
  events: Array<{ event_time: string; event_type: string; span_id: string }>,
  traceTailRunning: boolean,
): Set<string> {
  if (!traceTailRunning) return new Set();
  const sorted = sortEventsByTime(events);
  const counts = new Map<string, number>();
  const bump = (id: string, delta: number) => {
    if (!id) return;
    counts.set(id, (counts.get(id) ?? 0) + delta);
  };
  for (const ev of sorted) {
    const id = ev.span_id;
    switch (ev.event_type) {
      case "tool_call_started":
      case "model_call_started":
      case "reducer_started":
        bump(id, 1);
        break;
      case "tool_call_completed":
      case "tool_call_failed":
      case "model_call_completed":
      case "model_call_failed":
      case "reducer_completed":
      case "reducer_failed":
        bump(id, -1);
        break;
      default:
        break;
    }
  }
  const out = new Set<string>();
  for (const [id, c] of counts) {
    if (c > 0) out.add(id);
  }
  return out;
}
