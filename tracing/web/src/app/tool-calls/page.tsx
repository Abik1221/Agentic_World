import Link from "next/link";

import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult } from "@/lib/pyyol-lens-api";

type ToolRow = {
  trace_id: string;
  tool_name: string;
  status: string;
  error_message: string;
  total_tokens: number;
  estimated_cost: number;
};

export default async function ToolCallsPage() {
  const result = await fetchQueryResult<ToolRow[]>("/v1/tool-calls");
  const rows = result.ok ? result.data : [];
  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Tool Calls</h1>
          <p>Inspect tools as first-class spans with cost and error visibility.</p>
        </div>
      </section>
      {!result.ok ? <Unavailable title="Tool-call data unavailable" /> : (
      <section className="panel">
        <p className="table-count">{rows.length.toLocaleString()} tool calls</p>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Trace</th>
                <th>Tool</th>
                <th>Status</th>
                <th>Tokens</th>
                <th>Cost</th>
                <th>Error</th>
              </tr>
            </thead>
            <tbody>
              {rows.length ? (
                rows.map((row, index) => (
                  <tr key={`${row.trace_id}-${row.tool_name}-${index}`}>
                    <td className="mono">
                      {row.trace_id ? (
                        <Link className="trace-link" href={`/traces/${encodeURIComponent(row.trace_id)}`}>
                          {row.trace_id}
                        </Link>
                      ) : (
                        "-"
                      )}
                    </td>
                    <td>{row.tool_name || "-"}</td>
                    <td>
                      <span className={`status ${row.status === "error" ? "status-error" : "status-ok"}`}>
                        {row.status || "ok"}
                      </span>
                    </td>
                    <td>{row.total_tokens?.toLocaleString?.() ?? row.total_tokens ?? 0}</td>
                    <td>${Number(row.estimated_cost ?? 0).toFixed(4)}</td>
                    <td>{row.error_message || "-"}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={6} className="empty-state">
                    No tool call spans recorded yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
      )}
    </div>
  );
}
