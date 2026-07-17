"use client";

import { useDeferredValue, useState } from "react";
import type { EventRow } from "@/lib/pyyol-lens-api";

export default function EventsFeed({
  initialRows,
  initialQuery,
}: {
  initialRows: EventRow[];
  initialQuery: string;
}) {
  const [query, setQuery] = useState(initialQuery);
  const deferred = useDeferredValue(query);
  const rows = initialRows.filter((row) => {
    const value = deferred.trim().toLowerCase();
    if (!value) return true;
    return (
      row.event_type.toLowerCase().includes(value) ||
      row.trace_id.toLowerCase().includes(value) ||
      (row.model ?? "").toLowerCase().includes(value) ||
      (row.tool_name ?? "").toLowerCase().includes(value) ||
      (row.error_message ?? "").toLowerCase().includes(value)
    );
  });

  return (
    <div className="panel">
      <h2>Event Search</h2>
      <p className="table-count">
        Showing {rows.length.toLocaleString()} of {initialRows.length.toLocaleString()} events
      </p>
      <input
        className="feed-search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        placeholder="Search event type, trace, model, tool, or error"
      />
      <div className="table-wrap" style={{ marginTop: "1rem" }}>
        <table>
          <thead>
            <tr>
              <th>When</th>
              <th>Event</th>
              <th>Trace</th>
              <th>Status</th>
              <th>Model / Tool</th>
            </tr>
          </thead>
          <tbody>
            {rows.length ? (
              rows.map((row, index) => (
                <tr key={`${row.event_id}-${index}`}>
                  <td>{new Date(row.event_time).toLocaleString()}</td>
                  <td>
                    <strong>{row.event_type}</strong>
                    {row.error_message ? <div className="muted row-compact">{row.error_message}</div> : null}
                  </td>
                  <td className="mono">{row.trace_id}</td>
                  <td>
                    <span className={`status ${row.status === "error" ? "status-error" : "status-ok"}`}>
                      {row.status || "ok"}
                    </span>
                  </td>
                  <td>{row.model || row.tool_name || "-"}</td>
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={5} className="empty-state">
                  No matching events for this filter.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
