"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import type { EventRow } from "@/lib/pyyol-lens-api";

export default function EventsFeed({
  initialRows,
  initialQuery,
}: {
  initialRows: EventRow[];
  initialQuery: string;
}) {
  const router = useRouter();
  const [query, setQuery] = useState(initialQuery);

  // Drive the actual search on the SERVER: push the term to ?q= (debounced) so the
  // page re-runs its /v1/search/events query for it. Previously the box only filtered
  // the rows already fetched for the default query, so searching any other term showed
  // nothing. Skip the first run (query still equals what the server just used) to avoid
  // a redundant refetch on mount.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    const handle = setTimeout(() => {
      const term = query.trim();
      router.replace(term ? `/events?q=${encodeURIComponent(term)}` : "/events", { scroll: false });
    }, 350);
    return () => clearTimeout(handle);
  }, [query, router]);

  return (
    <div className="panel">
      <h2>Event Search</h2>
      <p className="table-count">
        Showing {initialRows.length.toLocaleString()} event{initialRows.length === 1 ? "" : "s"}
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
            {initialRows.length ? (
              initialRows.map((row, index) => (
                <tr key={`${row.event_id}-${index}`}>
                  <td>{new Date(row.event_time).toLocaleString()}</td>
                  <td>
                    <strong>{row.event_type}</strong>
                    {row.error_message ? <div className="muted row-compact">{row.error_message}</div> : null}
                  </td>
                  <td className="mono">
                    {row.trace_id ? (
                      <Link className="trace-link" href={`/traces/${encodeURIComponent(row.trace_id)}`}>
                        {row.trace_id}
                      </Link>
                    ) : (
                      "—"
                    )}
                  </td>
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
                  {query.trim() ? "No matching events for this search." : "No events yet."}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
