import Link from "next/link";

import { Pager } from "@/components/Pager";
import { Unavailable } from "@/components/Unavailable";
import { fetchQueryResult, type MatchListResponse } from "@/lib/pyyol-lens-api";
import { GAMES, coins, ktoks, usd, withParam } from "@/lib/benchmark-format";

const PAGE = 50;

export default async function GamesPage({
  searchParams,
}: {
  searchParams: Promise<{ game?: string; status?: string; offset?: string }>;
}) {
  const sp = await searchParams;
  const game = GAMES.includes(sp.game as (typeof GAMES)[number]) ? sp.game : undefined;
  const status = sp.status === "finished" || sp.status === "in_progress" ? sp.status : undefined;
  const offset = Math.max(0, Number(sp.offset) || 0);
  const q = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
  if (game) q.set("game", game);
  if (status) q.set("status", status);

  const result = await fetchQueryResult<MatchListResponse>(`/v1/matches?${q.toString()}`);
  const rows = result.ok ? (result.data.matches ?? []) : [];
  const total = result.ok ? result.data.total : 0;
  const params = { game, status };

  return (
    <div className="page">
      <section className="hero">
        <div>
          <h1>Games</h1>
          <p>
            Every ingested arena match, newest first. Open a row to inspect the log, table talk,
            who played, money path, and per-model cost.
          </p>
        </div>
      </section>

      <div className="chip-row">
        <Link className={`filter-chip${!game ? " filter-chip-active" : ""}`} href={withParam("/games", params, "game", undefined)}>
          All games
        </Link>
        {GAMES.map((g) => (
          <Link
            key={g}
            className={`filter-chip${game === g ? " filter-chip-active" : ""}`}
            href={withParam("/games", params, "game", g)}
          >
            {g}
          </Link>
        ))}
        <Link
          className={`filter-chip${!status ? " filter-chip-active" : ""}`}
          href={withParam("/games", { game, offset: undefined }, "status", undefined)}
        >
          Any status
        </Link>
        {(["in_progress", "finished"] as const).map((s) => (
          <Link
            key={s}
            className={`filter-chip${status === s ? " filter-chip-active" : ""}`}
            href={withParam("/games", { game }, "status", s)}
          >
            {s.replace("_", " ")}
          </Link>
        ))}
      </div>

      {!result.ok ? (
        <Unavailable title="Match list unavailable" />
      ) : (
        <section className="panel">
          <div className="console-header">
            <h2>Matches</h2>
            <p className="table-count">{rows.length.toLocaleString()} on this page</p>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Match</th>
                  <th>Game</th>
                  <th>Status</th>
                  <th>Players</th>
                  <th>Stake</th>
                  <th>Winner</th>
                  <th>Tokens</th>
                  <th>Cost</th>
                  <th>Started</th>
                </tr>
              </thead>
              <tbody>
                {rows.length ? (
                  rows.map((m) => (
                    <tr key={m.match_id}>
                      <td className="mono">
                        <Link className="trace-link" href={`/games/${encodeURIComponent(m.match_id)}`}>
                          {m.match_id}
                        </Link>
                      </td>
                      <td>{m.game || "—"}</td>
                      <td>
                        <span className={m.status === "finished" ? "status-ok" : "status-warn"}>
                          {m.status.replace("_", " ")}
                        </span>
                      </td>
                      <td className="mono">{(m.agents ?? []).length ? (m.agents ?? []).join(", ") : "—"}</td>
                      <td>{m.bid ? coins(m.bid) : <span className="muted">—</span>}</td>
                      <td className="mono">{m.winner_agent || <span className="muted">—</span>}</td>
                      <td>{m.tokens ? ktoks(m.tokens) : "—"}</td>
                      <td>{m.cost_usd ? usd(m.cost_usd) : "—"}</td>
                      <td className="muted">{m.started_at ? new Date(m.started_at).toISOString().replace("T", " ").slice(0, 19) : "—"}</td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={9} className="empty-state">
                      No matches in this window. Telemetry lands as games are played.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <Pager base="/games" params={params} total={total} limit={PAGE} offset={offset} />
        </section>
      )}
    </div>
  );
}
