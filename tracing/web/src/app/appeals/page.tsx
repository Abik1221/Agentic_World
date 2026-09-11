import Link from "next/link";

import { Pager } from "@/components/Pager";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type AppealListResponse } from "@/lib/pyyol-lens-api";

const PAGE = 50;

export default async function AppealsPage({
  searchParams,
}: {
  searchParams: Promise<{ match?: string; offset?: string }>;
}) {
  const sp = await searchParams;
  const match = sp.match?.trim() || undefined;
  const offset = Math.max(0, Number(sp.offset) || 0);
  const q = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
  if (match) q.set("match", match);

  const result = await fetchQueryResult<AppealListResponse>(`/v1/appeals?${q.toString()}`);
  const rows = result.ok ? (result.data.appeals ?? []) : [];
  const total = result.ok ? result.data.total : 0;

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Appeals</h1>
          <p>
            Disputes filed against arena matches. Open a match to walk the log, conversation,
            money path, and model spend that an appeal actually turns on.
          </p>
        </div>
      </section>

      {!result.ok ? (
        <Unavailable
          title="Appeals feed unavailable"
          detail="This reads dispute.opened events ingested into Lens. If the query API is up and this is empty, no disputes have been filed (or they were not ingested)."
        />
      ) : (
        <section className="panel">
          <div className="console-header">
            <h2>Open &amp; historical disputes</h2>
            <p className="table-count">{rows.length.toLocaleString()} on this page</p>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>When</th>
                  <th>Dispute</th>
                  <th>Kind</th>
                  <th>Match</th>
                  <th>Agent</th>
                  <th>Status</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {rows.length ? (
                  rows.map((a) => (
                    <tr key={`${a.dispute_id}-${a.at}`}>
                      <td className="muted">{a.at ? new Date(a.at).toISOString().replace("T", " ").slice(0, 19) : "—"}</td>
                      <td className="mono">{a.dispute_id}</td>
                      <td>{a.kind}</td>
                      <td className="mono">
                        {a.match_id ? (
                          <Link className="trace-link" href={`/games/${encodeURIComponent(a.match_id)}`}>
                            {a.match_id}
                          </Link>
                        ) : (
                          "—"
                        )}
                      </td>
                      <td className="mono">{a.agent_id || "—"}</td>
                      <td>
                        <span className={a.status === "open" ? "status-warn" : "status-ok"}>{a.status}</span>
                      </td>
                      <td>
                        {typeof a.detail?.detail === "string"
                          ? a.detail.detail
                          : a.error || "—"}
                      </td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={7} className="empty-state">
                      No disputes ingested. When a player files an appeal, it lands here from the
                      arena outbox (dispute.opened) — not a stub list.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <Pager base="/appeals" params={{ match }} total={total} limit={PAGE} offset={offset} />
        </section>
      )}
    </div>
  );
}
