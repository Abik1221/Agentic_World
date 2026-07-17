import type { EventRow, SpanNode, TraceSummary } from "@/lib/pyyol-lens-api";
import {
  formatOffsetFromStart,
  parseTraceStartMs,
  preorderExecutionOrder,
  sortEventsByTime,
  spanRoleLabel,
} from "@/lib/trace-order";

export const TRACE_EXPORT_SCHEMA = "pyyol-lens-trace-export/1";

const MD_PAYLOAD_MAX_CHARS = 2000;

export function syntheticTraceDoneSpanId(ev: EventRow): string {
  return `ze:trace_completed:${ev.event_id}`;
}

/** Group events by span_id; optional synthetic bucket for UI completion span. */
export function buildEventsBySpan(
  events: EventRow[],
  traceCompleted: EventRow | null,
): Record<string, EventRow[]> {
  const by: Record<string, EventRow[]> = {};
  for (const ev of events) {
    if (!ev.span_id) continue;
    (by[ev.span_id] ??= []).push(ev);
  }
  if (traceCompleted) {
    by[syntheticTraceDoneSpanId(traceCompleted)] = [traceCompleted];
  }
  for (const id of Object.keys(by)) {
    by[id] = sortEventsByTime(by[id]!);
  }
  return by;
}

function eventRemark(ev: EventRow): string {
  const t = ev.event_type;
  if (t === "model_call_completed")
    return "LLM call finished — payload may include full prompt/response.";
  if (t === "model_call_started") return "LLM call started.";
  if (t === "model_call_failed") return "LLM call failed — see error_message.";
  if (t === "model_call_retried") return "LLM call retried after a failure.";
  if (t === "tool_call_completed") return "Tool finished — see payload for result preview.";
  if (t === "tool_call_started") return "Tool invocation started.";
  if (t === "tool_call_failed") return "Tool failed — see error_message.";
  if (t === "reducer_started") return "Reducer started on upstream artifact.";
  if (t === "reducer_completed") return "Reducer produced a derived artifact.";
  if (t === "reducer_failed") return "Reducer failed.";
  if (t === "artifact_written") return "Artifact persisted.";
  if (t === "artifact_read") return "Artifact read.";
  if (t === "trace_completed") return "Run completed successfully.";
  if (t === "trace_failed") return "Run failed.";
  if (t === "trace_started") return "Trace / run started.";
  return `Telemetry: ${t}`;
}

function flattenSpan(span: SpanNode): Omit<SpanNode, "children"> {
  const { children: _c, ...rest } = span;
  return rest;
}

function spanRemarks(
  span: SpanNode,
  executionOrder: Map<string, number>,
  traceStartMs: number | null,
): string[] {
  const out: string[] = [];
  const role = spanRoleLabel(span);
  if (role) out.push(`Role: ${role}.`);
  out.push(`Status: ${span.status || "ok"}.`);
  const ord = executionOrder.get(span.span_id);
  if (ord != null) out.push(`Execution order (preorder, same as rail #): ${ord}.`);
  const off = formatOffsetFromStart(traceStartMs, span.started_at);
  if (off) out.push(`Started ${off} after trace start.`);
  out.push(
    `Wall latency ${Math.round(span.latency_ms)}ms; tokens ${span.total_tokens}; est. cost $${span.estimated_cost.toFixed(4)}.`,
  );
  if (span.provider || span.model) {
    out.push(`Model: ${[span.provider, span.model].filter(Boolean).join(" ") || "—"}.`);
  }
  if ((span.artifact_ids_in?.length ?? 0) > 0) {
    out.push(`Artifacts read / consumed: ${span.artifact_ids_in!.join(", ")}.`);
  }
  if ((span.artifact_ids_out?.length ?? 0) > 0) {
    out.push(`Artifacts written: ${span.artifact_ids_out!.join(", ")}.`);
  }
  if (span.error_message) out.push(`Error: ${span.error_message}`);
  return out;
}

export type TraceExportJsonNode = {
  execution_order: number | null;
  parent_span_id: string;
  span_id: string;
  remarks: string[];
  span: Omit<SpanNode, "children">;
  events: Array<EventRow & { _remark: string }>;
  children: TraceExportJsonNode[];
};

export type TraceExportJson = {
  _meta: {
    schema_version: string;
    exported_at: string;
    trace_id: string;
    remarks: string[];
  };
  trace_summary: TraceSummary;
  events_chronological: Array<EventRow & { _remark: string }>;
  execution_tree: TraceExportJsonNode[];
};

export type TraceExportInput = {
  trace: TraceSummary;
  displayTree: SpanNode[];
  events: EventRow[];
  traceCompleted: EventRow | null;
};

function buildTreeNodes(
  nodes: SpanNode[],
  executionOrder: Map<string, number>,
  traceStartMs: number | null,
  bySpan: Record<string, EventRow[]>,
): TraceExportJsonNode[] {
  return nodes.map((span) => {
    const list = bySpan[span.span_id] ?? [];
    const withRemarks = list.map((e) => ({
      ...e,
      _remark: eventRemark(e),
    }));
    return {
      execution_order: executionOrder.get(span.span_id) ?? null,
      parent_span_id: span.parent_span_id,
      span_id: span.span_id,
      remarks: spanRemarks(span, executionOrder, traceStartMs),
      span: flattenSpan(span),
      events: withRemarks,
      children: buildTreeNodes(
        span.children ?? [],
        executionOrder,
        traceStartMs,
        bySpan,
      ),
    };
  });
}

export function buildExportJson(input: TraceExportInput): TraceExportJson {
  const { trace, displayTree, events, traceCompleted } = input;
  const exportedAt = new Date().toISOString();
  const traceStartMs = parseTraceStartMs(trace.started_at);
  const executionOrder = preorderExecutionOrder(displayTree);
  const bySpan = buildEventsBySpan(events, traceCompleted);
  const remarks = [
    "execution_tree is a preorder forest: parent nodes appear before descendants (same order as the Execution tree rail #1→n).",
    "Each node includes `events` for that span_id only (sorted by event_time).",
    "events_chronological is the full feed for the trace in time order.",
    "_remark on events is a short human hint, not from the backend.",
  ];
  const events_chronological = sortEventsByTime(events).map((e) => ({
    ...e,
    _remark: eventRemark(e),
  }));
  const execution_tree = buildTreeNodes(
    displayTree,
    executionOrder,
    traceStartMs,
    bySpan,
  );
  return {
    _meta: {
      schema_version: TRACE_EXPORT_SCHEMA,
      exported_at: exportedAt,
      trace_id: trace.trace_id,
      remarks,
    },
    trace_summary: trace,
    events_chronological,
    execution_tree,
  };
}

function mdHeading(level: number, text: string): string {
  const cap = Math.min(Math.max(level, 2), 4);
  return `${"#".repeat(cap)} ${text}\n\n`;
}

function truncateJsonForMd(payload: Record<string, unknown> | undefined): string {
  if (!payload || typeof payload !== "object") return "";
  try {
    const s = JSON.stringify(payload, null, 0);
    if (s.length <= MD_PAYLOAD_MAX_CHARS) return s;
    return `${s.slice(0, MD_PAYLOAD_MAX_CHARS)}…\n<!-- truncated: ${s.length} chars total -->`;
  } catch {
    return "[unserializable payload]";
  }
}

function walkMd(
  nodes: SpanNode[],
  depth: number,
  executionOrder: Map<string, number>,
  traceStartMs: number | null,
  bySpan: Record<string, EventRow[]>,
  lines: string[],
): void {
  for (const span of nodes) {
    const ord = executionOrder.get(span.span_id);
    const title = `${ord != null ? `#${ord} ` : ""}${span.step_name || span.span_type || span.span_id}`;
    lines.push(mdHeading(depth + 2, title));
    lines.push(`- **Span id:** \`${span.span_id}\`\n`);
    lines.push(`- **Parent span id:** \`${span.parent_span_id || "—"}\`\n`);
    lines.push(`- **Children:** ${span.children?.length ?? 0} nested span(s) below.\n`);
    for (const r of spanRemarks(span, executionOrder, traceStartMs)) {
      lines.push(`- ${r}\n`);
    }
    lines.push("\n**Events (this span)**\n\n");
    const evs = bySpan[span.span_id] ?? [];
    if (!evs.length) {
      lines.push("_No events recorded for this span._\n\n");
    } else {
      let i = 1;
      for (const ev of evs) {
        lines.push(`${i}. \`${ev.event_time}\` **${ev.event_type}** — _${eventRemark(ev)}_\n`);
        lines.push(`   - span_id: \`${ev.span_id}\`\n`);
        if (ev.error_message) lines.push(`   - error: ${ev.error_message}\n`);
        if (ev.payload_json && Object.keys(ev.payload_json).length > 0) {
          lines.push("   - payload (may be truncated in Markdown export):\n\n");
          lines.push("```json\n");
          lines.push(truncateJsonForMd(ev.payload_json));
          lines.push("\n```\n\n");
        }
        i += 1;
      }
    }
    if (span.children?.length) {
      walkMd(span.children, depth + 1, executionOrder, traceStartMs, bySpan, lines);
    }
  }
}

export function buildExportMarkdown(input: TraceExportInput): string {
  const { trace, displayTree, events, traceCompleted } = input;
  const exportedAt = new Date().toISOString();
  const traceStartMs = parseTraceStartMs(trace.started_at);
  const executionOrder = preorderExecutionOrder(displayTree);
  const bySpan = buildEventsBySpan(events, traceCompleted);
  const lines: string[] = [];
  lines.push(`# Trace export\n\n`);
  lines.push(
    `> **Read me:** This file mirrors the pyyol-lens trace view: summary, full chronological telemetry, then the **execution tree in preorder** (same order as the left rail #1→n). Lineage is explicit via parent/child headings.\n\n`,
  );
  lines.push(`- **Trace id:** \`${trace.trace_id}\`\n`);
  lines.push(`- **Exported at:** ${exportedAt}\n`);
  lines.push(`- **Schema:** \`${TRACE_EXPORT_SCHEMA}\`\n\n`);
  lines.push("---\n\n");
  lines.push("## Trace summary\n\n");
  lines.push("| Field | Value |\n");
  lines.push("| --- | --- |\n");
  lines.push(`| status | ${trace.status || "ok"} |\n`);
  lines.push(`| latency_ms | ${trace.latency_ms} |\n`);
  lines.push(`| total_tokens | ${trace.total_tokens} |\n`);
  lines.push(`| total_cost | ${trace.total_cost} |\n`);
  lines.push(`| environment | ${trace.environment} |\n`);
  lines.push(`| project_id | ${trace.project_id} |\n`);
  lines.push(`| started_at | ${trace.started_at} |\n`);
  lines.push(`| ended_at | ${trace.ended_at} |\n`);
  lines.push(`| event_count | ${trace.event_count} |\n\n`);
  if (trace.error_message) {
    lines.push(`**Trace-level error:** ${trace.error_message}\n\n`);
  }
  lines.push("---\n\n");
  lines.push("## Chronological event log\n\n");
  lines.push(
    "_All events for this trace in time order. Payloads in Markdown are truncated per event for readability; use JSON export for full payloads._\n\n",
  );
  const chrono = sortEventsByTime(events);
  chrono.forEach((ev, idx) => {
    lines.push(
      `${idx + 1}. \`${ev.event_time}\` **${ev.event_type}** — _${eventRemark(ev)}_ — span \`${ev.span_id || "—"}\`\n`,
    );
    if (ev.payload_json && Object.keys(ev.payload_json).length > 0) {
      lines.push("\n   ```json\n   ");
      lines.push(
        truncateJsonForMd(ev.payload_json).replace(/\n/g, "\n   "),
      );
      lines.push("\n   ```\n\n");
    }
  });
  lines.push("\n---\n\n");
  lines.push("## Execution tree (preorder)\n\n");
  if (!displayTree.length) {
    lines.push("_No spans in tree._\n");
  } else {
    walkMd(displayTree, 0, executionOrder, traceStartMs, bySpan, lines);
  }
  return lines.join("");
}

export function sanitizeTraceFilename(traceId: string, ext: string): string {
  const safe = traceId.replace(/[^a-zA-Z0-9._-]+/g, "_").slice(0, 120);
  return `trace-${safe || "unknown"}.${ext}`;
}

export function downloadFile(filename: string, mime: string, body: string): void {
  if (typeof window === "undefined") return;
  const blob = new Blob([body], { type: mime });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}
