"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import type { TraceSummary } from "@/lib/pyyol-lens-api";

type Props = {
  traces: TraceSummary[];
};

type RangeValue = "all" | "24h" | "7d";
type StatusValue = "all" | "ok" | "error";

export default function TracesConsole({ traces }: Props) {
  const [referenceNow] = useState(() => Date.now());
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<StatusValue>("all");
  const [project, setProject] = useState("all");
  const [range, setRange] = useState<RangeValue>("all");

  const projects = useMemo(() => {
    const set = new Set<string>();
    for (const trace of traces) {
      if (trace.project_id) set.add(trace.project_id);
    }
    return Array.from(set).sort((a, b) => a.localeCompare(b));
  }, [traces]);

  const filtered = useMemo(() => {
    const text = query.trim().toLowerCase();

    return traces.filter((trace) => {
      if (status !== "all" && (trace.status || "ok") !== status) return false;
      if (project !== "all" && (trace.project_id || "") !== project) return false;

      if (range !== "all" && trace.started_at) {
        const started = new Date(trace.started_at).getTime();
        const maxAge = range === "24h" ? 24 * 60 * 60 * 1000 : 7 * 24 * 60 * 60 * 1000;
        if (Number.isFinite(started) && referenceNow - started > maxAge) return false;
      }

      if (!text) return true;
      return (
        (trace.trace_id || "").toLowerCase().includes(text) ||
        (trace.project_id || "").toLowerCase().includes(text) ||
        (trace.model || "").toLowerCase().includes(text) ||
        (trace.status || "").toLowerCase().includes(text)
      );
    });
  }, [traces, query, status, project, range, referenceNow]);

  return (
    <section className="panel">
      <div className="console-header">
        <h2>Recent Traces</h2>
        <p className="table-count">{filtered.length.toLocaleString()} of {traces.length.toLocaleString()} traces</p>
      </div>

      <div className="toolbar">
        <input
          className="feed-search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search trace ID, project, model, or status"
        />
        <select className="toolbar-select" value={status} onChange={(event) => setStatus(event.target.value as StatusValue)}>
          <option value="all">All Statuses</option>
          <option value="ok">Only OK</option>
          <option value="error">Only Errors</option>
        </select>
        <select className="toolbar-select" value={project} onChange={(event) => setProject(event.target.value)}>
          <option value="all">All Projects</option>
          {projects.map((name) => (
            <option key={name} value={name}>{name}</option>
          ))}
        </select>
        <select className="toolbar-select" value={range} onChange={(event) => setRange(event.target.value as RangeValue)}>
          <option value="all">All Time</option>
          <option value="24h">Last 24h</option>
          <option value="7d">Last 7d</option>
        </select>
      </div>

      <div className="saved-views">
        <button type="button" className="saved-view-btn" onClick={() => { setStatus("all"); setRange("all"); }}>
          All Traces
        </button>
        <button type="button" className="saved-view-btn" onClick={() => { setStatus("error"); setRange("24h"); }}>
          Errors (24h)
        </button>
        <button type="button" className="saved-view-btn" onClick={() => { setStatus("ok"); setRange("7d"); }}>
          Healthy (7d)
        </button>
      </div>

      <div className="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Trace</th>
              <th>Status</th>
              <th>Project</th>
              <th>Model</th>
              <th>Tokens</th>
              <th>Cost</th>
              <th>Latency</th>
              <th>Started</th>
            </tr>
          </thead>
          <tbody>
            {filtered.length ? (
              filtered.map((trace) => (
                <tr key={trace.trace_id}>
                  <td className="mono">
                    <Link className="trace-link" href={`/traces/${trace.trace_id}`}>
                      {trace.trace_id}
                    </Link>
                    {trace.trace_id.startsWith("match_") && (
                      <>
                        {" "}
                        <Link
                          className="agent-link"
                          href={`/matches/${encodeURIComponent(trace.trace_id)}`}
                          title="Per-move reasoning, decisions & tokens"
                        >
                          decision trail →
                        </Link>
                      </>
                    )}
                  </td>
                  <td>
                    <span className={`status ${trace.status === "error" ? "status-error" : "status-ok"}`}>
                      {trace.status || "ok"}
                    </span>
                  </td>
                  <td>{trace.project_id || "-"}</td>
                  <td>{trace.model || "-"}</td>
                  <td>{trace.total_tokens.toLocaleString()}</td>
                  <td>${trace.total_cost.toFixed(4)}</td>
                  <td>{Math.round(trace.latency_ms)}ms</td>
                  <td>{trace.started_at ? new Date(trace.started_at).toLocaleString() : "-"}</td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={8} className="empty-state">
                  No traces match the current filters.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}
  