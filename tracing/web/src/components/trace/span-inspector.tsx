"use client";

import { useMemo } from "react";
import type { EventRow, SpanNode } from "@/lib/pyyol-lens-api";
import {
  formatOffsetFromStart,
  sortEventsByTime,
  spanRoleLabel,
} from "@/lib/trace-order";
import {
  normalizedAssistantOutput,
  normalizedParallelCalls,
  normalizedPromptMessages,
  normalizedToolFields,
  normalizedUsage,
  stringifyContent,
} from "@/lib/trace-payload";
import { CollapsibleSection, JsonOrText } from "./json-or-text";

function formatMs(ms: number) {
  if (ms < 1) return "<1ms";
  if (ms < 1000) return `${ms.toFixed(0)}ms`;
  return `${(ms / 1000).toFixed(2)}s`;
}

function usageTokensLine(ev: EventRow): string {
  const parts: string[] = [];
  if (ev.total_tokens != null && ev.total_tokens > 0) parts.push(`${ev.total_tokens} total`);
  if (ev.prompt_tokens != null && ev.prompt_tokens > 0) parts.push(`${ev.prompt_tokens} prompt`);
  if (ev.completion_tokens != null && ev.completion_tokens > 0) parts.push(`${ev.completion_tokens} completion`);
  return parts.length ? parts.join(" · ") : "";
}

/**
 * Group consecutive LLM and tool events that look like attempts of the same
 * logical call. Order:
 *   1. Prefer explicit `retry_of` / `attempt` from payload_json (emitted by
 *      AgentService for LLM retries — Issue 3).
 *   2. Fall back to (step_name + tool_name) when no explicit chain is set,
 *      so historic traces still group retries reasonably.
 *
 * Returns a flat array where each group is either one event or a chain of
 * attempts the inspector can render under a single collapsible heading.
 */
type AttemptGroup = { key: string; events: EventRow[] };
function groupAttempts(events: EventRow[]): AttemptGroup[] {
  const byId = new Map<string, EventRow>();
  for (const ev of events) byId.set(ev.event_id, ev);

  // Build retry chains via retry_of: child -> parent_attempt_id.
  const parentByEvent = new Map<string, string>();
  for (const ev of events) {
    const p = ev.payload_json ?? {};
    const ref = typeof p.retry_of === "string" ? p.retry_of : "";
    if (ref) parentByEvent.set(ev.event_id, ref);
  }

  const claimed = new Set<string>();
  const groups: AttemptGroup[] = [];
  for (const ev of events) {
    if (claimed.has(ev.event_id)) continue;

    // Resolve to chain root.
    let rootId = ev.event_id;
    while (parentByEvent.has(rootId)) {
      const next = parentByEvent.get(rootId)!;
      if (!byId.has(next) || next === rootId) break;
      rootId = next;
    }

    const chain: EventRow[] = [];
    const walk = (id: string) => {
      const node = byId.get(id);
      if (!node || claimed.has(node.event_id)) return;
      chain.push(node);
      claimed.add(node.event_id);
      // Find children pointing at this id.
      for (const candidate of events) {
        if (parentByEvent.get(candidate.event_id) === id) {
          walk(candidate.event_id);
        }
      }
    };
    walk(rootId);

    if (chain.length === 0) {
      chain.push(ev);
      claimed.add(ev.event_id);
    }

    groups.push({ key: rootId, events: chain });
  }
  return groups;
}

function attemptNumber(ev: EventRow, fallback: number): number {
  const a = (ev.payload_json ?? {}).attempt;
  if (typeof a === "number" && Number.isFinite(a) && a > 0) return Math.floor(a);
  return fallback;
}

export default function SpanInspector({
  span,
  events,
  executionOrder,
  traceStartMs,
  onSelectSpan,
}: {
  span: SpanNode | null;
  events: EventRow[];
  executionOrder: Map<string, number>;
  traceStartMs: number | null;
  onSelectSpan: (spanId: string) => void;
}) {
  const sortedAll = useMemo(() => sortEventsByTime(events), [events]);

  const modelCallEvents = useMemo(
    () =>
      sortEventsByTime(
        sortedAll.filter((e) => /^model_call_(started|completed|failed|retried)/.test(e.event_type)),
      ),
    [sortedAll],
  );

  const meteringEvents = useMemo(
    () =>
      sortedAll.filter(
        (e) => e.event_type === "token_estimated" || e.event_type === "usage_reported",
      ),
    [sortedAll],
  );

  const toolEvents = useMemo(
    () => sortEventsByTime(sortedAll.filter((e) => /^tool_call_(started|completed|failed)/.test(e.event_type))),
    [sortedAll],
  );

  const artifactWriteEvents = useMemo(
    () => sortEventsByTime(sortedAll.filter((e) => e.event_type === "artifact_written")),
    [sortedAll],
  );

  const traceDoneEvent = useMemo(
    () => sortedAll.find((e) => e.event_type === "trace_completed"),
    [sortedAll],
  );

  // Show only model_call_completed / model_call_failed in the LLM section
  // (the started + retried events are handled inside the attempt grouping).
  const llmGroups = useMemo(() => {
    const subset = modelCallEvents.filter((e) =>
      /^model_call_(completed|failed|retried)$/.test(e.event_type),
    );
    return groupAttempts(subset);
  }, [modelCallEvents]);

  const toolGroups = useMemo(() => {
    const subset = toolEvents.filter((e) =>
      /^tool_call_(completed|failed)$/.test(e.event_type),
    );
    return groupAttempts(subset);
  }, [toolEvents]);

  const outcomes = useMemo(() => {
    const out: { label: string; value: unknown }[] = [];
    for (const ev of modelCallEvents.filter((e) => e.event_type === "model_call_completed")) {
      const p = ev.payload_json ?? {};
      const preview = normalizedAssistantOutput(p);
      out.push({ label: "LLM output", value: preview ?? "(see LLM events below)" });
    }
    for (const ev of modelCallEvents.filter((e) => e.event_type === "model_call_failed")) {
      out.push({
        label: "LLM failed",
        value: ev.error_message || ev.status || "failed",
      });
    }
    for (const ev of toolEvents) {
      if (ev.event_type === "tool_call_failed") {
        out.push({
          label: `Tool failed · ${ev.tool_name || "?"}`,
          value: ev.error_message || ev.status || "error",
        });
        continue;
      }
      if (ev.event_type !== "tool_call_completed") continue;
      const tf = normalizedToolFields(ev.payload_json ?? {});
      out.push({
        label: `Tool · ${ev.tool_name || tf.tool || "?"}`,
        value: tf.success === false ? tf.error || ev.error_message || "failed" : tf.result,
      });
    }
    return out;
  }, [modelCallEvents, toolEvents]);

  if (!span) {
    return (
      <div className="wb-panel">
        <p className="wb-muted" style={{ margin: 0, fontSize: 13 }}>
          Select a span in the tree, graph, or timeline. Each selection shows execution order, status, children,
          LLM prompts and models, token usage, tool inputs and results.
        </p>
      </div>
    );
  }

  const seq = executionOrder.get(span.span_id);
  const offset = formatOffsetFromStart(traceStartMs, span.started_at);
  const role = spanRoleLabel(span);
  const children = span.children ?? [];

  return (
    <div className="wb-panel wb-scroll" style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <CollapsibleSection title="Overview" defaultOpen>
        <div className="wb-inspector-head">
          {seq != null ? (
            <span className="wb-order-pill" title="Execution order (tree preorder, first → last)">
              #{seq}
              {offset ? <span className="wb-order-offset">{offset}</span> : null}
            </span>
          ) : null}
          <span className={`wb-status wb-status-${span.status === "error" ? "err" : "ok"}`}>
            {span.status || "ok"}
          </span>
          {role ? <span className="wb-chip accent">{role}</span> : null}
          {span.span_type ? (
            <span className="wb-chip dim" title="Span type">
              {span.span_type}
            </span>
          ) : null}
        </div>
        <h3 className="wb-heading">{span.step_name || span.span_type || span.span_id}</h3>
        <div className="wb-muted mono" style={{ fontSize: 11, marginTop: 4 }}>
          {span.span_id}
        </div>
        <div className="wb-chip-row" style={{ marginTop: 10 }}>
          {span.task_kind ? <span className="wb-chip">{span.task_kind}</span> : null}
          {span.archetype ? <span className="wb-chip">{span.archetype}</span> : null}
          {span.scope ? <span className="wb-chip dim">{span.scope}</span> : null}
          {span.subagent_id ? (
            <span className="wb-chip dim mono" title="Subagent">
              sub:{span.subagent_id.slice(0, 8)}…
            </span>
          ) : null}
        </div>
        <dl className="wb-dl">
          <div>
            <dt>Wall time</dt>
            <dd>{formatMs(span.latency_ms)}</dd>
          </div>
          <div>
            <dt>LLM (rollup)</dt>
            <dd>{[span.provider, span.model].filter(Boolean).join(" ") || "—"}</dd>
          </div>
          <div>
            <dt>Tool (rollup)</dt>
            <dd>{span.tool_name || "—"}</dd>
          </div>
          <div>
            <dt>Tokens (rollup)</dt>
            <dd>{span.total_tokens > 0 ? span.total_tokens.toLocaleString() : "—"}</dd>
          </div>
          <div>
            <dt>Cost</dt>
            <dd>{span.estimated_cost > 0 ? `$${span.estimated_cost.toFixed(4)}` : "—"}</dd>
          </div>
        </dl>
        {span.error_message ? (
          <p className="wb-error-text" style={{ margin: "8px 0 0", fontSize: 13 }}>
            {span.error_message}
          </p>
        ) : null}
      </CollapsibleSection>

      {traceDoneEvent && span.span_type === "trace_completion" ? (
        <CollapsibleSection title="Final response" defaultOpen>
          <dl className="wb-dl compact single">
            <div>
              <dt>Trace wall (from event)</dt>
              <dd>
                {traceDoneEvent.latency_ms != null && traceDoneEvent.latency_ms > 0
                  ? formatMs(traceDoneEvent.latency_ms)
                  : "—"}
              </dd>
            </div>
            <div>
              <dt>Final send (persist → stream)</dt>
              <dd>
                {typeof traceDoneEvent.payload_json?.final_send_latency_ms === "number"
                  ? formatMs(traceDoneEvent.payload_json.final_send_latency_ms as number)
                  : "—"}
              </dd>
            </div>
          </dl>
          {typeof traceDoneEvent.payload_json?.final_assistant_markdown === "string" &&
          (traceDoneEvent.payload_json.final_assistant_markdown as string).length > 0 ? (
            <div style={{ marginTop: 8 }}>
              <JsonOrText
                value={traceDoneEvent.payload_json.final_assistant_markdown as string}
                previewChars={0}
              />
            </div>
          ) : (
            <p className="wb-muted" style={{ fontSize: 12, marginTop: 6 }}>
              No final markdown payload on this completion event.
            </p>
          )}
        </CollapsibleSection>
      ) : null}

      {outcomes.length > 0 && (
        <CollapsibleSection title="Results · outcomes" badge={`(${outcomes.length})`} defaultOpen>
          <ul className="wb-outcome-list">
            {outcomes.map((h, i) => (
              <li key={i} style={{ marginBottom: 8 }}>
                <strong>{h.label}</strong>
                <div style={{ marginTop: 4 }}>
                  <JsonOrText value={h.value} previewChars={0} />
                </div>
              </li>
            ))}
          </ul>
        </CollapsibleSection>
      )}

      {children.length > 0 && (
        <CollapsibleSection title={`Spawned children (${children.length})`} defaultOpen>
          <p className="wb-muted" style={{ fontSize: 12, margin: "0 0 8px" }}>
            Steps that ran inside this span (tools, reducers, reads/writes, nested LLM calls). Click to inspect.
          </p>
          <ul className="wb-child-list">
            {children.map((ch) => {
              const cSeq = executionOrder.get(ch.span_id);
              const cRole = spanRoleLabel(ch);
              const cOff = formatOffsetFromStart(traceStartMs, ch.started_at);
              return (
                <li key={ch.span_id}>
                  <button
                    type="button"
                    className="wb-child-btn"
                    onClick={() => onSelectSpan(ch.span_id)}
                  >
                    <span className="wb-child-seq">{cSeq != null ? `#${cSeq}` : "—"}</span>
                    <span className={`wb-dot wb-dot-${ch.status === "error" ? "err" : "ok"}`} />
                    <span className="wb-child-label">{ch.step_name || ch.span_type}</span>
                    {cRole ? <span className="wb-chip dim tiny">{cRole}</span> : null}
                    <span className="wb-child-meta">
                      {cOff ?? ""}
                      {cOff ? " · " : ""}
                      {formatMs(ch.latency_ms)}
                      {ch.total_tokens > 0 ? ` · ${ch.total_tokens} tok` : ""}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        </CollapsibleSection>
      )}

      {(span.artifact_ids_in?.length ?? 0) > 0 && (
        <CollapsibleSection title="Artifacts read (inputs)" defaultOpen>
          <ul className="wb-list">
            {span.artifact_ids_in!.map((id) => (
              <li key={id} className="mono">
                {id}
              </li>
            ))}
          </ul>
        </CollapsibleSection>
      )}

      {(span.artifact_ids_out?.length ?? 0) > 0 && (
        <CollapsibleSection title="Artifacts written (outputs)" defaultOpen>
          <ul className="wb-list">
            {span.artifact_ids_out!.map((id) => (
              <li key={id} className="mono">
                {id}
              </li>
            ))}
          </ul>
        </CollapsibleSection>
      )}

      {llmGroups.length > 0 && (
        <CollapsibleSection
          title="LLM calls (this span)"
          badge={`(${llmGroups.length})`}
          defaultOpen
        >
          <p className="wb-muted" style={{ fontSize: 12, margin: "0 0 8px" }}>
            Prompts, provider/model, wall time, and token breakdown per telemetry event.
            Failed attempts are grouped with their retry under one heading (Issue 3 — retry correlation).
          </p>
          {llmGroups.map((g) => (
            <LlmGroupView key={g.key} group={g} />
          ))}
        </CollapsibleSection>
      )}

      {meteringEvents.length > 0 && (
        <CollapsibleSection title="Token metering" defaultOpen={false}>
          {meteringEvents.map((ev) => (
            <div key={ev.event_id} className="wb-subcard wb-subcard-muted">
              <div className="wb-subcard-head">
                <span className="mono">{ev.event_type}</span>
                <span className="wb-muted">{usageTokensLine(ev) || "—"}</span>
              </div>
            </div>
          ))}
        </CollapsibleSection>
      )}

      {toolGroups.length > 0 && (
        <CollapsibleSection
          title="Tool calls (this span)"
          badge={`(${toolGroups.length})`}
          defaultOpen
        >
          <p className="wb-muted" style={{ fontSize: 12, margin: "0 0 8px" }}>
            Inputs, results, duration, and status per tool telemetry event. Retried tool calls are grouped.
          </p>
          {toolGroups.map((g) => (
            <ToolGroupView key={g.key} group={g} />
          ))}
        </CollapsibleSection>
      )}

      {artifactWriteEvents.length > 0 && (
        <CollapsibleSection
          title="Artifact writes (this span)"
          badge={`(${artifactWriteEvents.length})`}
          defaultOpen={false}
        >
          <p className="wb-muted" style={{ fontSize: 12, margin: "0 0 8px" }}>
            Inline <span className="mono">artifact_data</span> appears when the API telemetry byte cap allows it.
          </p>
          {artifactWriteEvents.map((ev) => {
            const p = ev.payload_json ?? {};
            const data = p.artifact_data;
            const trunc = p.telemetry_truncation;
            return (
              <div key={ev.event_id} className="wb-subcard">
                <div className="wb-subcard-head">
                  <span className="mono">{ev.event_type}</span>
                  <span className="wb-event-time">{ev.event_time}</span>
                </div>
                <dl className="wb-dl compact single">
                  <div>
                    <dt>Artifact</dt>
                    <dd className="mono">{String(p.artifact_id ?? "—")}</dd>
                  </div>
                  <div>
                    <dt>Producer</dt>
                    <dd>
                      {String(p.producer_kind ?? "")} {String(p.producer_name ?? "")}
                    </dd>
                  </div>
                  <div>
                    <dt>Bytes</dt>
                    <dd>{p.byte_size != null ? String(p.byte_size) : "—"}</dd>
                  </div>
                </dl>
                {trunc ? (
                  <p className="wb-muted" style={{ fontSize: 12, marginTop: 6 }}>
                    Telemetry: {String(trunc)}
                  </p>
                ) : null}
                {data != null ? (
                  <div style={{ marginTop: 8 }}>
                    <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
                      artifact_data
                    </div>
                    <JsonOrText value={data} previewChars={0} />
                  </div>
                ) : null}
              </div>
            );
          })}
        </CollapsibleSection>
      )}

      {sortedAll.length === 0 && (
        <p className="wb-muted" style={{ fontSize: 13 }}>
          No events recorded for this span.
        </p>
      )}

      <CollapsibleSection
        title="Raw events"
        badge={`(${sortedAll.length})`}
        defaultOpen={false}
      >
        <JsonOrText
          value={sortedAll.map((e) => ({
            event_id: e.event_id,
            event_type: e.event_type,
            event_time: e.event_time,
            status: e.status,
            latency_ms: e.latency_ms,
            provider: e.provider,
            model: e.model,
            tool_name: e.tool_name,
            total_tokens: e.total_tokens,
            prompt_tokens: e.prompt_tokens,
            completion_tokens: e.completion_tokens,
            payload_json: e.payload_json,
          }))}
        />
      </CollapsibleSection>
    </div>
  );
}

function LlmGroupView({ group }: { group: AttemptGroup }) {
  const events = group.events;
  if (events.length === 1) {
    return <LlmAttemptCard ev={events[0]} attemptLabel={null} />;
  }
  // Sort so attempt 1 renders first.
  const ordered = [...events].sort(
    (a, b) => attemptNumber(a, 1) - attemptNumber(b, 1),
  );
  const lastIsOk = ordered[ordered.length - 1]?.status !== "error";
  return (
    <details
      open={!lastIsOk}
      className="wb-subcard"
      style={{ padding: 0, border: "1px solid var(--wb-border)" }}
    >
      <summary
        style={{
          padding: "8px 10px",
          cursor: "pointer",
          listStyle: "none",
          fontSize: 12,
          display: "flex",
          alignItems: "center",
          gap: 8,
        }}
      >
        <span style={{ fontWeight: 700 }}>Retried LLM call</span>
        <span className="wb-muted">
          {ordered.length} attempts · final={lastIsOk ? "ok" : "error"}
        </span>
      </summary>
      <div style={{ padding: 10, paddingTop: 0 }}>
        {ordered.map((ev, idx) => (
          <LlmAttemptCard key={ev.event_id} ev={ev} attemptLabel={attemptNumber(ev, idx + 1)} />
        ))}
      </div>
    </details>
  );
}

function LlmAttemptCard({
  ev,
  attemptLabel,
}: {
  ev: EventRow;
  attemptLabel: number | null;
}) {
  const p = ev.payload_json ?? {};
  const msgs = normalizedPromptMessages(p);
  const out = normalizedAssistantOutput(p);
  const usage = normalizedUsage(p);
  const parallel = normalizedParallelCalls(p);
  const provider = ev.provider || (p.provider as string | undefined);
  const model = ev.model || (p.model as string | undefined);
  const tokenLine = (() => {
    const parts: string[] = [];
    if (ev.total_tokens) parts.push(`${ev.total_tokens} total`);
    if (ev.prompt_tokens) parts.push(`${ev.prompt_tokens} prompt`);
    if (ev.completion_tokens) parts.push(`${ev.completion_tokens} completion`);
    return parts.join(" · ");
  })();
  const wall = ev.latency_ms && ev.latency_ms > 0 ? formatMs(ev.latency_ms) : null;

  return (
    <div className="wb-subcard" style={{ marginTop: 8 }}>
      <div className="wb-subcard-head">
        <span className="mono">
          {attemptLabel != null ? `Attempt ${attemptLabel} · ` : ""}
          {ev.event_type}
        </span>
        <span className="wb-event-time">{ev.event_time}</span>
      </div>
      <dl className="wb-dl compact single">
        <div>
          <dt>Provider · model</dt>
          <dd>{[provider, model].filter(Boolean).join(" ") || "—"}</dd>
        </div>
        <div>
          <dt>Time (telemetry)</dt>
          <dd>{wall ?? "—"}</dd>
        </div>
        <div>
          <dt>Tokens</dt>
          <dd>{tokenLine || "—"}</dd>
        </div>
        <div>
          <dt>Status</dt>
          <dd>{ev.status}</dd>
        </div>
      </dl>
      {ev.status && ev.status !== "ok" ? (
        <p className="wb-error-text" style={{ margin: "6px 0 0", fontSize: 12 }}>
          {ev.error_message || ev.status}
        </p>
      ) : null}
      {msgs && msgs.length > 0 && (
        <details style={{ marginTop: 8 }}>
          <summary
            className="wb-muted"
            style={{ fontSize: 11, cursor: "pointer", padding: "4px 0" }}
          >
            Prompt sent to the model ({msgs.length} messages)
          </summary>
          <div style={{ marginTop: 6 }}>
            {msgs.map((m, i) => (
              <div key={i} style={{ marginBottom: 8 }}>
                <div
                  style={{
                    fontSize: 11,
                    color: "var(--wb-muted-strong)",
                    marginBottom: 4,
                    fontWeight: 700,
                  }}
                >
                  {m.role ?? "?"}
                </div>
                <JsonOrText value={stringifyContent(m.content)} previewChars={0} />
              </div>
            ))}
          </div>
        </details>
      )}
      {parallel && parallel.length > 0 && (
        <div style={{ marginTop: 8 }}>
          <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
            Tool calls planned from this LLM step
          </div>
          <JsonOrText value={parallel} />
        </div>
      )}
      {out != null && (
        <div style={{ marginTop: 8 }}>
          <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
            Model output
          </div>
          <JsonOrText value={out} previewChars={0} />
        </div>
      )}
      {usage && Object.keys(usage).length > 0 && (
        <div style={{ marginTop: 8 }}>
          <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
            Usage object
          </div>
          <JsonOrText value={usage} />
        </div>
      )}
    </div>
  );
}

function ToolGroupView({ group }: { group: AttemptGroup }) {
  const events = group.events;
  if (events.length === 1) {
    return <ToolAttemptCard ev={events[0]} attemptLabel={null} />;
  }
  const ordered = [...events].sort(
    (a, b) => attemptNumber(a, 1) - attemptNumber(b, 1),
  );
  const lastIsOk = ordered[ordered.length - 1]?.status !== "error";
  return (
    <details
      open={!lastIsOk}
      className="wb-subcard"
      style={{ padding: 0, border: "1px solid var(--wb-border)" }}
    >
      <summary
        style={{
          padding: "8px 10px",
          cursor: "pointer",
          listStyle: "none",
          fontSize: 12,
          display: "flex",
          alignItems: "center",
          gap: 8,
        }}
      >
        <span style={{ fontWeight: 700 }}>Retried tool call</span>
        <span className="wb-muted">
          {ordered.length} attempts · final={lastIsOk ? "ok" : "error"}
        </span>
      </summary>
      <div style={{ padding: 10, paddingTop: 0 }}>
        {ordered.map((ev, idx) => (
          <ToolAttemptCard
            key={ev.event_id}
            ev={ev}
            attemptLabel={attemptNumber(ev, idx + 1)}
          />
        ))}
      </div>
    </details>
  );
}

function ToolAttemptCard({
  ev,
  attemptLabel,
}: {
  ev: EventRow;
  attemptLabel: number | null;
}) {
  const p = ev.payload_json ?? {};
  const tf = normalizedToolFields(p);
  const toolLabel = ev.tool_name || tf.tool || "?";
  const wallFromPayload = tf.durationMs != null ? formatMs(tf.durationMs) : null;
  const wallFromEvent =
    ev.latency_ms != null && ev.latency_ms > 0 ? formatMs(ev.latency_ms) : null;

  return (
    <div className="wb-subcard" style={{ marginTop: 8 }}>
      <div className="wb-subcard-head">
        <span className="mono">
          {attemptLabel != null ? `Attempt ${attemptLabel} · ` : ""}
          {ev.event_type}
        </span>
        <span style={{ fontWeight: 700 }}>{toolLabel}</span>
      </div>
      <span className="wb-event-time">{ev.event_time}</span>
      <dl className="wb-dl compact single">
        <div>
          <dt>Duration</dt>
          <dd>{wallFromPayload ?? wallFromEvent ?? "—"}</dd>
        </div>
        <div>
          <dt>Status</dt>
          <dd>{ev.status}</dd>
        </div>
        <div>
          <dt>Success</dt>
          <dd>{tf.success === undefined ? "—" : tf.success ? "yes" : "no"}</dd>
        </div>
      </dl>
      {tf.args != null && (
        <div style={{ marginTop: 6 }}>
          <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
            Input / arguments
          </div>
          <JsonOrText value={tf.args} previewChars={0} />
        </div>
      )}
      {tf.result != null && (
        <div style={{ marginTop: 6 }}>
          <div className="wb-muted" style={{ fontSize: 11, marginBottom: 6 }}>
            Result
          </div>
          <JsonOrText value={tf.result} previewChars={0} />
        </div>
      )}
      {tf.error ? <p className="wb-error-text">{tf.error}</p> : null}
    </div>
  );
}
