"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import TraceFlameGraph from "@/app/traces/[traceId]/TraceFlameGraph";
import type { EventRow, SpanNode, TraceSummary } from "@/lib/pyyol-lens-api";
import SpanInspector from "@/components/trace/span-inspector";
import TraceLineageGraph from "@/components/trace/trace-lineage-graph";
import TraceLiveRefresh from "@/components/trace/trace-live-refresh";
import TraceExportMenu from "@/components/trace/trace-export-menu";
import {
  formatOffsetFromStart,
  parseTraceStartMs,
  preorderExecutionOrder,
  sortEventsByTime,
  spanRoleLabel,
  activeSpanIdsFromEvents,
} from "@/lib/trace-order";

type RailRow = { span: SpanNode; depth: number };

function findSpan(nodes: SpanNode[], id: string): SpanNode | null {
  for (const n of nodes) {
    if (n.span_id === id) return n;
    const sub = findSpan(n.children ?? [], id);
    if (sub) return sub;
  }
  return null;
}

function flattenVisible(nodes: SpanNode[], collapsed: Set<string>, depth = 0): RailRow[] {
  const out: RailRow[] = [];
  for (const n of nodes) {
    out.push({ span: n, depth });
    const has = (n.children?.length ?? 0) > 0;
    if (has && !collapsed.has(n.span_id)) {
      out.push(...flattenVisible(n.children ?? [], collapsed, depth + 1));
    }
  }
  return out;
}

function firstLeaf(nodes: SpanNode[]): SpanNode | null {
  if (!nodes.length) return null;
  const [n] = nodes;
  if ((n.children?.length ?? 0) === 0) return n;
  return firstLeaf(n.children ?? []) ?? n;
}

function syntheticTraceDoneSpanId(ev: EventRow) {
  return `ze:trace_completed:${ev.event_id}`;
}

/** UI-only span so “final response” lives in the execution tree (not a giant header card). */
function buildSyntheticCompletionSpan(trace: TraceSummary, ev: EventRow): SpanNode {
  const p = ev.payload_json ?? {};
  const finalSend =
    typeof p.final_send_latency_ms === "number" ? Math.max(0, Math.round(p.final_send_latency_ms)) : 0;
  const t = Date.parse(ev.event_time);
  const iso = Number.isFinite(t) ? ev.event_time : trace.started_at;
  return {
    span_id: syntheticTraceDoneSpanId(ev),
    trace_id: trace.trace_id,
    parent_span_id: "",
    span_type: "trace_completion",
    step_name: "trace_completed · final response",
    status: "ok",
    started_at: iso,
    ended_at: iso,
    latency_ms: finalSend,
    provider: "",
    model: "",
    tool_name: "",
    total_tokens: 0,
    estimated_cost: 0,
    error_type: "",
    error_message: "",
    task_kind: "completion",
  };
}

function attachCompletionToTree(roots: SpanNode[], child: SpanNode): SpanNode[] {
  if (!roots.length) return [child];
  return roots.map((r, i) => (i === 0 ? { ...r, children: [...(r.children ?? []), child] } : r));
}

export default function TraceWorkbench({
  trace,
  tree,
  events,
}: {
  trace: TraceSummary;
  tree: SpanNode[];
  events: EventRow[];
}) {
  const initialCollapsed = useMemo(() => {
    const s = new Set<string>();
    const mark = (nodes: SpanNode[], depth: number) => {
      for (const n of nodes) {
        if ((n.children?.length ?? 0) > 0 && depth >= 2) {
          s.add(n.span_id);
        }
        mark(n.children ?? [], depth + 1);
      }
    };
    mark(tree, 0);
    return s;
  }, [tree]);

  const [collapsed, setCollapsed] = useState<Set<string>>(() => initialCollapsed);
  const [selectedSpanId, setSelectedSpanId] = useState<string | null>(null);
  const [centerTab, setCenterTab] = useState<"graph" | "timeline">("graph");
  const [rightTab, setRightTab] = useState<"span" | "events">("span");
  const railRef = useRef<HTMLDivElement>(null);

  const traceCompleted = useMemo(() => {
    const completed = events.filter((e) => e.event_type === "trace_completed");
    if (!completed.length) return null;
    const sorted = sortEventsByTime(completed);
    return sorted[sorted.length - 1]!;
  }, [events]);

  const traceTailRunning = useMemo(() => {
    const sorted = sortEventsByTime(events);
    return !sorted.some(
      (e) =>
        e.event_type === "trace_completed" || e.event_type === "trace_failed",
    );
  }, [events]);

  const activeSpanIds = useMemo(
    () => activeSpanIdsFromEvents(events, traceTailRunning),
    [events, traceTailRunning],
  );

  const displayTree = useMemo(() => {
    if (!traceCompleted) return tree;
    const syn = buildSyntheticCompletionSpan(trace, traceCompleted);
    return attachCompletionToTree(tree, syn);
  }, [tree, trace, traceCompleted]);

  useEffect(() => {
    setSelectedSpanId((prev) => {
      if (prev && findSpan(displayTree, prev)) return prev;
      const leaf = firstLeaf(displayTree);
      return leaf?.span_id ?? null;
    });
  }, [displayTree]);

  const traceStartMs = useMemo(() => parseTraceStartMs(trace.started_at), [trace.started_at]);

  const timelineEndCapMs = useMemo(() => {
    if (!traceCompleted) return null;
    const t = Date.parse(traceCompleted.event_time);
    return Number.isFinite(t) ? t : null;
  }, [traceCompleted]);

  const headerLatencyMs = useMemo(() => {
    const rollup = trace.latency_ms;
    if (!traceCompleted) return rollup;
    const evWall = traceCompleted.latency_ms;
    if (evWall != null && evWall > 0) {
      if (rollup > evWall * 2 && rollup > 120_000) return evWall;
      return Math.min(rollup, evWall);
    }
    if (traceStartMs != null && timelineEndCapMs != null && timelineEndCapMs >= traceStartMs) {
      const dt = timelineEndCapMs - traceStartMs;
      if (rollup > dt * 2 && dt > 0 && rollup > 120_000) return dt;
    }
    return rollup;
  }, [trace, traceCompleted, traceStartMs, timelineEndCapMs]);

  const executionOrder = useMemo(() => preorderExecutionOrder(displayTree), [displayTree]);

  const eventsBySpan = useMemo(() => {
    const m: Record<string, EventRow[]> = {};
    for (const ev of events) {
      if (!ev.span_id) continue;
      (m[ev.span_id] ??= []).push(ev);
    }
    if (traceCompleted) {
      m[syntheticTraceDoneSpanId(traceCompleted)] = [traceCompleted];
    }
    return m;
  }, [events, traceCompleted]);

  const railRows = useMemo(
    () => flattenVisible(displayTree, collapsed),
    [displayTree, collapsed],
  );

  const selectedSpan = selectedSpanId ? findSpan(displayTree, selectedSpanId) : null;
  const selectedEvents = selectedSpanId ? eventsBySpan[selectedSpanId] ?? [] : [];

  useEffect(() => {
    if (!selectedSpanId || !railRef.current) return;
    const row = railRef.current.querySelector(`[data-span-row="${CSS.escape(selectedSpanId)}"]`);
    row?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [selectedSpanId, railRows]);

  const toggleCollapsed = useCallback((spanId: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(spanId)) next.delete(spanId);
      else next.add(spanId);
      return next;
    });
  }, []);

  const focusRailRow = useCallback(
    (delta: number) => {
      if (!railRows.length) return;
      const idx = Math.max(
        0,
        railRows.findIndex((r) => r.span.span_id === selectedSpanId),
      );
      const next = railRows[Math.min(railRows.length - 1, Math.max(0, idx + delta))];
      if (next) setSelectedSpanId(next.span.span_id);
    },
    [railRows, selectedSpanId],
  );

  useEffect(() => {
    const el = railRef.current;
    if (!el) return;
    const onKey = (e: KeyboardEvent) => {
      if (document.activeElement !== el && !el.contains(document.activeElement)) return;
      if (e.key === "ArrowDown") {
        e.preventDefault();
        focusRailRow(1);
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        focusRailRow(-1);
      }
      if (e.key === "Enter" || e.key === " ") {
        const row = railRows.find((r) => r.span.span_id === selectedSpanId);
        if (row && (row.span.children?.length ?? 0) > 0) {
          e.preventDefault();
          toggleCollapsed(row.span.span_id);
        }
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [focusRailRow, railRows, selectedSpanId, toggleCollapsed]);

  return (
    <div className="trace-workbench trace-detail-page page">
      <header className="trace-workbench-header">
        <div>
          <h1 className="trace-workbench-title">Trace</h1>
          <p className="wb-muted trace-workbench-sub">
            <span className="mono">{trace.trace_id}</span>
            {" · "}
            {trace.project_id} / {trace.environment}
            {trace.trace_id.startsWith("match_") && (
              <>
                {" · "}
                <Link className="agent-link" href={`/games/${encodeURIComponent(trace.trace_id.replace(/^match_/, ""))}`}>
                  game →
                </Link>
              </>
            )}
          </p>
        </div>
        <div className="trace-workbench-header-right">
          <div className="trace-workbench-header-actions">
            <TraceExportMenu
              trace={trace}
              displayTree={displayTree}
              events={events}
              traceCompleted={traceCompleted}
            />
          </div>
          <div className="trace-workbench-kpis">
            <div className="wb-kpi">
              <span className="wb-kpi-label">Status</span>
              <span className="wb-kpi-value">{trace.status || "ok"}</span>
            </div>
            <div className="wb-kpi">
              <span className="wb-kpi-label">Latency</span>
              <span className="wb-kpi-value">{Math.round(headerLatencyMs)}ms</span>
            </div>
            <div className="wb-kpi">
              <span className="wb-kpi-label">Tokens</span>
              <span className="wb-kpi-value">{trace.total_tokens.toLocaleString()}</span>
            </div>
            <div className="wb-kpi">
              <span className="wb-kpi-label">Cost</span>
              <span className="wb-kpi-value">${trace.total_cost.toFixed(4)}</span>
            </div>
          </div>
        </div>
      </header>

      <TraceLiveRefresh enabled={traceTailRunning} intervalMs={5000} />

      <div className="trace-workbench-main">
        <aside className="trace-workbench-rail" aria-label="Span hierarchy">
          <div className="wb-rail-head">
            <span className="wb-eyebrow">Execution tree</span>
            <p className="wb-rail-intro wb-muted">
              Order <span className="mono">#1→n</span> is traversal order (what started first at each branch).
            </p>
            <div className="wb-segmented">
              <button type="button" onClick={() => setCollapsed(new Set())}>
                Expand all
              </button>
              <button type="button" onClick={() => setCollapsed(new Set(initialCollapsed))}>
                Reset depth
              </button>
            </div>
          </div>
          <div
            ref={railRef}
            tabIndex={0}
            className="wb-rail-body wb-scroll keyboard-zone"
          >
            {railRows.length === 0 ? (
              <p className="wb-muted">No spans.</p>
            ) : (
              railRows.map(({ span, depth }) => {
                const hasCh = (span.children?.length ?? 0) > 0;
                const open = hasCh && !collapsed.has(span.span_id);
                const sel = span.span_id === selectedSpanId;
                const err = span.status === "error";
                const pending = traceTailRunning && activeSpanIds.has(span.span_id);
                const dotClass = err ? "err" : pending ? "pending" : "ok";
                const ord = executionOrder.get(span.span_id);
                const off = formatOffsetFromStart(traceStartMs, span.started_at);
                const role = spanRoleLabel(span);
                return (
                  <div
                    key={span.span_id}
                    role="button"
                    tabIndex={-1}
                    data-span-row={span.span_id}
                    className={`wb-rail-row wb-rail-row-${sel ? "selected" : "idle"}${err ? " wb-rail-row-err" : ""}`}
                    style={{ paddingLeft: 10 + depth * 14 }}
                    onClick={() => setSelectedSpanId(span.span_id)}
                  >
                    {hasCh ? (
                      <button
                        type="button"
                        className="wb-rail-chevron"
                        aria-label={open ? "Collapse" : "Expand"}
                        onClick={(e) => {
                          e.stopPropagation();
                          toggleCollapsed(span.span_id);
                        }}
                      >
                        {open ? "▼" : "▶"}
                      </button>
                    ) : (
                      <span className="wb-rail-chevron wb-rail-chevron-spacer" />
                    )}
                    <div className="wb-rail-row-main">
                      <div className="wb-rail-title">
                        {ord != null ? (
                          <span className="wb-order-pill rail" title="Execution order">
                            #{ord}
                          </span>
                        ) : null}
                        <span
                          className={`wb-dot wb-dot-${dotClass}`}
                          title={err ? span.status : pending ? "In progress" : span.status || "ok"}
                        />
                        <span className="wb-rail-label">{span.step_name || span.span_type}</span>
                        {role ? (
                          <span className="wb-chip accent tiny" title="Span role">
                            {role}
                          </span>
                        ) : null}
                      </div>
                      <div className="wb-rail-meta">
                        {off ? <span title="After trace start">{off}</span> : null}
                        <span>{Math.round(span.latency_ms)}ms</span>
                        {span.total_tokens > 0 ? (
                          <span>{span.total_tokens.toLocaleString()} tok</span>
                        ) : null}
                        {hasCh ? (
                          <span className="wb-muted" title="Direct children">
                            {span.children!.length}↓
                          </span>
                        ) : null}
                        {span.task_kind ? <span className="wb-chip">{span.task_kind}</span> : null}
                        {span.archetype ? <span className="wb-chip dim">{span.archetype}</span> : null}
                      </div>
                    </div>
                  </div>
                );
              })
            )}
          </div>
          <p className="wb-rail-hint wb-muted">↑↓ navigate · Enter toggle folder</p>
        </aside>

        <section className="trace-workbench-center">
          <div className="wb-center-tabs">
            <button
              type="button"
              className={centerTab === "graph" ? "active" : ""}
              onClick={() => setCenterTab("graph")}
            >
              Lineage graph
            </button>
            <button
              type="button"
              className={centerTab === "timeline" ? "active" : ""}
              onClick={() => setCenterTab("timeline")}
            >
              Timeline
            </button>
          </div>
          <div className="trace-workbench-center-body">
            {centerTab === "graph" ? (
              displayTree.length ? (
                <TraceLineageGraph
                  tree={displayTree}
                  selectedSpanId={selectedSpanId}
                  onSelectSpan={setSelectedSpanId}
                  executionOrder={executionOrder}
                />
              ) : (
                <p className="wb-muted">No spans to graph.</p>
              )
            ) : (
              <TraceFlameGraph
                tree={displayTree}
                events={events}
                selectedSpanId={selectedSpanId}
                onSelectSpan={setSelectedSpanId}
                showDetailPanel={false}
                timelineEndCapMs={timelineEndCapMs}
              />
            )}
          </div>
        </section>

        <aside className="trace-workbench-inspector">
          <div className="wb-center-tabs wb-inspector-tabs">
            <button
              type="button"
              className={rightTab === "span" ? "active" : ""}
              onClick={() => setRightTab("span")}
            >
              Span detail
            </button>
            <button
              type="button"
              className={rightTab === "events" ? "active" : ""}
              onClick={() => setRightTab("events")}
            >
              All events ({events.length})
            </button>
          </div>
          {rightTab === "span" ? (
            <SpanInspector
              span={selectedSpan}
              events={selectedEvents}
              executionOrder={executionOrder}
              traceStartMs={traceStartMs}
              onSelectSpan={(id) => {
                setSelectedSpanId(id);
                setRightTab("span");
              }}
            />
          ) : (
            <div className="wb-panel wb-scroll" style={{ padding: 0 }}>
              <p className="wb-muted wb-event-caption">
                Chronological feed for the whole trace. Click a row to open that span’s detail (prompts, tools,
                tokens).
              </p>
              <table className="wb-event-table">
                <thead>
                  <tr>
                    <th className="wb-col-seq">#</th>
                    <th>When</th>
                    <th>Event</th>
                    <th>Model / tool</th>
                    <th>Δ start</th>
                    <th>Latency</th>
                    <th>Tok</th>
                    <th>Span</th>
                    <th>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {events.map((ev, i) => {
                    const spanOrd =
                      ev.event_type === "trace_completed" &&
                      traceCompleted &&
                      ev.event_id === traceCompleted.event_id
                        ? executionOrder.get(syntheticTraceDoneSpanId(ev))
                        : ev.span_id
                          ? executionOrder.get(ev.span_id)
                          : undefined;
                    const delta =
                      traceStartMs != null && ev.event_time
                        ? formatOffsetFromStart(traceStartMs, ev.event_time)
                        : null;
                    const mt =
                      ev.event_type.includes("model") || ev.model
                        ? [ev.provider, ev.model].filter(Boolean).join(" ")
                        : ev.tool_name || "";
                    return (
                      <tr
                        key={ev.event_id}
                        className={ev.span_id === selectedSpanId ? "wb-event-selected" : undefined}
                        onClick={() => {
                          if (ev.event_type === "trace_completed" && traceCompleted && ev.event_id === traceCompleted.event_id) {
                            setSelectedSpanId(syntheticTraceDoneSpanId(ev));
                          } else {
                            setSelectedSpanId(ev.span_id || null);
                          }
                          setRightTab("span");
                        }}
                        style={{ cursor: "pointer" }}
                      >
                        <td className="mono wb-muted wb-col-seq">{i + 1}</td>
                        <td className="mono wb-muted wb-event-when">{ev.event_time}</td>
                        <td>{ev.event_type}</td>
                        <td className="wb-event-mt">{mt || "—"}</td>
                        <td className="mono wb-muted">{delta ?? "—"}</td>
                        <td className="mono">
                          {ev.latency_ms != null && ev.latency_ms > 0 ? `${ev.latency_ms}ms` : "—"}
                        </td>
                        <td className="mono">
                          {ev.total_tokens != null && ev.total_tokens > 0 ? ev.total_tokens : "—"}
                        </td>
                        <td className="mono">
                          {ev.span_id ? (
                            <span title={ev.span_id}>
                              {spanOrd != null ? `#${spanOrd} ` : ""}
                              {ev.span_id.slice(0, 8)}…
                            </span>
                          ) : (
                            "—"
                          )}
                        </td>
                        <td>{ev.status}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </aside>
      </div>
    </div>
  );
}
