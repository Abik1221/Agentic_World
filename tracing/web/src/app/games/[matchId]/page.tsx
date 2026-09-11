import Link from "next/link";

import { Unavailable } from "@/components/Unavailable";
import {
  fetchArenaPublic,
  fetchQueryResult,
  type ArenaReplay,
  type ArenaRoster,
  type MatchCostResponse,
  type MatchLogEntry,
  type MatchMoneyResponse,
} from "@/lib/pyyol-lens-api";
import { coins, ktoks, ms, usd } from "@/lib/benchmark-format";
import { MatchTimeline, type TimelineEntry } from "@/app/matches/[matchId]/MatchTimeline";

type View = "overview" | "log" | "conversation" | "players" | "money" | "cost";
const VIEWS: { id: View; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "log", label: "Game log" },
  { id: "conversation", label: "Conversation" },
  { id: "players", label: "Who played" },
  { id: "money", label: "Money path" },
  { id: "cost", label: "Cost & models" },
];

type TokenUsage = {
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
};
type DecisionLogEntry = {
  round?: number;
  action?: string;
  outcome?: string;
  latency_ms?: number;
  rationale?: string;
  usage?: TokenUsage;
};
type BenchmarkPayload = {
  seat?: number;
  decisions?: number;
  legal_rate?: number;
  fallback_rate?: number;
  latency_avg_ms?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  result?: string;
  decision_log?: DecisionLogEntry[];
};
type MatchDecisions = {
  match_id: string;
  agents: { agent_id: string; game: string; benchmark: BenchmarkPayload }[];
};
type MatchTimelineResponse = { match_id: string; agents: string[]; entries: TimelineEntry[] };
type MatchLogsResponse = { match_id: string; entries: MatchLogEntry[]; count: number };

function outcomeClass(outcome: string | undefined): string {
  if (outcome === "ok") return "status-ok";
  if (outcome === "illegal_move" || outcome === "timeout") return "status-error";
  return "status-warn";
}

function str(d: Record<string, unknown> | undefined, k: string): string {
  const v = d?.[k];
  return typeof v === "string" ? v : "";
}

export default async function GameDetailPage({
  params,
  searchParams,
}: {
  params: Promise<{ matchId: string }>;
  searchParams: Promise<{ view?: string }>;
}) {
  const { matchId } = await params;
  const { view: viewRaw } = await searchParams;
  const id = decodeURIComponent(matchId);
  const view: View = VIEWS.some((v) => v.id === viewRaw) ? (viewRaw as View) : "overview";
  const withdrawn =
    id === "monopoly" || id.startsWith("mp_") || id.startsWith("match_mp_");

  if (withdrawn) {
    return (
      <div className="page">
        <section className="hero">
          <div>
            <p style={{ margin: 0 }}>
              <Link className="agent-link" href="/games">
                ← Games
              </Link>
            </p>
            <h1 className="mono">Match {id}</h1>
            <p>This match is from a withdrawn game and is not listed in Eye.</p>
          </div>
        </section>
      </div>
    );
  }

  const [dataRes, timelineRes, logsRes, costRes, moneyRes, replayRes, rosterRes] = await Promise.all([
    fetchQueryResult<MatchDecisions>(`/v1/matches/${encodeURIComponent(id)}/decisions`),
    fetchQueryResult<MatchTimelineResponse>(`/v1/matches/${encodeURIComponent(id)}/timeline`),
    fetchQueryResult<MatchLogsResponse>(`/v1/matches/${encodeURIComponent(id)}/logs?limit=2000`),
    fetchQueryResult<MatchCostResponse>(`/v1/matches/${encodeURIComponent(id)}/cost`),
    fetchQueryResult<MatchMoneyResponse>(`/v1/matches/${encodeURIComponent(id)}/money`),
    fetchArenaPublic<ArenaReplay>(`/v1/match/${encodeURIComponent(id)}/replay`),
    fetchArenaPublic<ArenaRoster>(`/v1/match/${encodeURIComponent(id)}/roster`),
  ]);

  const agents = dataRes.ok ? (dataRes.data.agents ?? []) : [];
  const entries = timelineRes.ok ? (timelineRes.data.entries ?? []) : [];
  const logs = logsRes.ok ? (logsRes.data.entries ?? []) : [];
  const cost = costRes.ok ? costRes.data : null;
  const money = moneyRes.ok ? moneyRes.data : null;
  const replay = replayRes.ok ? replayRes.data : null;
  const roster = rosterRes.ok ? (rosterRes.data.seats ?? replay?.roster ?? []) : (replay?.roster ?? []);
  const queryDown = !dataRes.ok && !timelineRes.ok && !logsRes.ok;
  const game = agents[0]?.game || entries[0]?.game || money?.game || "";
  const chat = entries.filter((e) => e.type === "agent_said" || e.type === "agent_say_rejected");

  return (
    <div className="page">
      <section className="hero">
        <div>
          <p style={{ margin: 0 }}>
            <Link className="agent-link" href="/games">
              ← Games
            </Link>
          </p>
          <h1 className="mono">Match {id}</h1>
          <p>
            {game ? `${game} · ` : ""}
            Inspect the log, conversation, roster, money path, and model spend for this match —
            start to finish.
          </p>
        </div>
      </section>

      <nav className="chip-row" aria-label="Match facets">
        {VIEWS.map((v) => (
          <Link
            key={v.id}
            className={`filter-chip${view === v.id ? " filter-chip-active" : ""}`}
            href={`/games/${encodeURIComponent(id)}?view=${v.id}`}
          >
            {v.label}
          </Link>
        ))}
        <Link className="filter-chip" href={`/traces/${encodeURIComponent(`match_${id}`)}`}>
          Trace workbench →
        </Link>
      </nav>

      {queryDown ? <Unavailable title="Match trail unavailable" /> : null}

      {view === "overview" && !queryDown && (
        <>
          <section className="cards">
            <article className="card">
              <p className="card-label">Decisions</p>
              <p className="card-value">{entries.filter((e) => e.type === "agent_decision").length}</p>
            </article>
            <article className="card">
              <p className="card-label">Chat lines</p>
              <p className="card-value">{chat.length}</p>
            </article>
            <article className="card">
              <p className="card-label">Tokens</p>
              <p className="card-value">{cost?.total_tokens ? ktoks(cost.total_tokens) : "—"}</p>
            </article>
            <article className="card">
              <p className="card-label">Model spend</p>
              <p className="card-value">{cost?.total_cost_usd ? usd(cost.total_cost_usd) : "—"}</p>
            </article>
            <article className="card">
              <p className="card-label">Stake</p>
              <p className="card-value">{money?.bid ? coins(money.bid) : "—"}</p>
            </article>
            <article className="card">
              <p className="card-label">Winner</p>
              <p className="card-value mono" style={{ fontSize: "0.95rem" }}>
                {money?.winner_agent || "—"}
              </p>
            </article>
          </section>
          <MatchTimeline entries={entries} />
          {agents.length === 0 && entries.length === 0 && logs.length === 0 && (
            <section className="panel">
              <p className="empty-state">
                No events recorded for this match yet. Decisions appear as each move happens;
                settlement totals appear when the match finishes.
              </p>
            </section>
          )}
        </>
      )}

      {view === "log" && (
        <section className="panel">
          <h2 className="panel-title">
            Full log <span className="muted">· {logs.length} telemetry events</span>
          </h2>
          <p className="muted" style={{ marginTop: 0 }}>
            Every ingested event from kickoff to settlement. Arena replay events
            {replay?.events?.length ? ` (${replay.events.length})` : ""} sit below when the public
            replay document is available.
          </p>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Type</th>
                  <th>Agent</th>
                  <th>Model</th>
                  <th>Detail</th>
                  <th>Tokens</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {logs.length ? (
                  logs.map((e) => (
                    <tr key={e.event_id}>
                      <td className="muted">{e.at ? new Date(e.at).toISOString().slice(11, 23) : "—"}</td>
                      <td className="mono">{e.type}</td>
                      <td className="mono">{e.agent_id || "—"}</td>
                      <td>{e.model || "—"}</td>
                      <td>
                        {str(e.detail, "action") ||
                          str(e.detail, "text") ||
                          str(e.detail, "rationale") ||
                          e.error ||
                          "—"}
                      </td>
                      <td>{e.tokens ? ktoks(e.tokens) : "—"}</td>
                      <td>
                        <span className={e.status === "error" ? "status-error" : "status-ok"}>
                          {e.status || "ok"}
                        </span>
                      </td>
                    </tr>
                  ))
                ) : (
                  <tr>
                    <td colSpan={7} className="empty-state">
                      No telemetry log for this match.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          {replay?.events && replay.events.length > 0 && (
            <>
              <h3 className="panel-title" style={{ marginTop: "1.2rem" }}>
                Arena replay <span className="muted">· {replay.events.length} engine events</span>
              </h3>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Seq</th>
                      <th>Type</th>
                      <th>Payload</th>
                    </tr>
                  </thead>
                  <tbody>
                    {replay.events.map((ev, i) => (
                      <tr key={i}>
                        <td>{ev.seq ?? i}</td>
                        <td className="mono">{String(ev.type ?? "event")}</td>
                        <td className="mono">{JSON.stringify(ev).slice(0, 280)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </section>
      )}

      {view === "conversation" && (
        <section className="panel">
          <h2 className="panel-title">Conversation</h2>
          {chat.length === 0 ? (
            <p className="empty-state">No table talk recorded for this match.</p>
          ) : (
            <ol className="eye-chat">
              {chat.map((e) => {
                const text = str(e.detail, "text");
                const rejected = e.type === "agent_say_rejected";
                return (
                  <li key={e.event_id} className={rejected ? "eye-chat-rejected" : ""}>
                    <div className="eye-chat-meta">
                      <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(e.agent_id)}`}>
                        {e.agent_id || "unknown"}
                      </Link>
                      <span className="muted">{e.at ? new Date(e.at).toISOString().slice(11, 19) : ""}</span>
                      {rejected ? <span className="status-warn">silenced</span> : null}
                    </div>
                    <p>{text || <span className="muted">(empty)</span>}</p>
                    {rejected && str(e.detail, "reason") ? (
                      <p className="muted">Reason: {str(e.detail, "reason")}</p>
                    ) : null}
                  </li>
                );
              })}
            </ol>
          )}
        </section>
      )}

      {view === "players" && (
        <section className="panel">
          <h2 className="panel-title">Who played</h2>
          {roster.length === 0 && agents.length === 0 ? (
            <p className="empty-state">
              Roster is empty. If the arena replay API is unreachable, seats still appear here once
              telemetry names an actor_id.
            </p>
          ) : (
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Seat</th>
                    <th>Agent</th>
                    <th>Name</th>
                    <th>Owner</th>
                    <th>Result</th>
                    <th>Decisions</th>
                  </tr>
                </thead>
                <tbody>
                  {(roster.length ? roster : agents.map((a, i) => ({ seat: a.benchmark?.seat ?? i, agent_id: a.agent_id, name: "", owner: "" }))).map(
                    (seat) => {
                      const bench = agents.find((a) => a.agent_id === seat.agent_id);
                      return (
                        <tr key={`${seat.seat}-${seat.agent_id}`}>
                          <td>{seat.seat}</td>
                          <td className="mono">
                            <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(seat.agent_id)}`}>
                              {seat.agent_id}
                            </Link>
                          </td>
                          <td>{seat.name || "—"}</td>
                          <td className="mono">{seat.owner || "—"}</td>
                          <td>{bench?.benchmark?.result || (money?.winner_agent === seat.agent_id ? "win" : "—")}</td>
                          <td>{bench?.benchmark?.decisions ?? "—"}</td>
                        </tr>
                      );
                    },
                  )}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      {view === "money" && (
        <section className="panel">
          <h2 className="panel-title">Money path</h2>
          {!money?.available ? (
            <p className="empty-state">
              No stake/settlement telemetry for this match yet. Competitive matches emit bid, pool,
              rake, and per-seat coins_delta when they finish. Sandbox matches do not move coins.
            </p>
          ) : (
            <>
              <section className="cards">
                <article className="card">
                  <p className="card-label">Stake / seat</p>
                  <p className="card-value">{coins(money.bid ?? 0)}</p>
                </article>
                <article className="card">
                  <p className="card-label">Pot</p>
                  <p className="card-value">{money.pool ? coins(money.pool) : "—"}</p>
                </article>
                <article className="card">
                  <p className="card-label">Rake</p>
                  <p className="card-value">
                    {money.rake_coins ? coins(money.rake_coins) : "—"}
                    {money.rake_pct ? <span className="muted"> · {money.rake_pct}%</span> : null}
                  </p>
                </article>
                <article className="card">
                  <p className="card-label">Winner</p>
                  <p className="card-value mono" style={{ fontSize: "0.95rem" }}>
                    {money.winner_agent || (money.settled ? "tie" : "unsettled")}
                  </p>
                </article>
              </section>
              <p className="muted">
                Each seat stakes the bid into escrow. On settlement the pot minus rake goes to the
                winner; losers are −bid. A zero coins_delta with a finished status is a tie or a
                voided match (integrity refund), not a missing number.
              </p>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Seat</th>
                      <th>Agent</th>
                      <th>Score</th>
                      <th>Distribution</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(money.seats ?? []).length ? (
                      (money.seats ?? []).map((s) => (
                        <tr key={s.agent_id}>
                          <td>{s.seat}</td>
                          <td className="mono">
                            <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(s.agent_id)}`}>
                              {s.agent_id}
                            </Link>
                            {money.winner_agent === s.agent_id ? " · winner" : ""}
                          </td>
                          <td>{s.score}</td>
                          <td className={s.coins_delta > 0 ? "status-ok" : s.coins_delta < 0 ? "status-error" : ""}>
                            {s.coins_delta > 0 ? "+" : ""}
                            {coins(s.coins_delta)}
                          </td>
                        </tr>
                      ))
                    ) : (
                      <tr>
                        <td colSpan={4} className="empty-state">
                          Settlement payload has a bid but no per-seat lines (matches finished
                          before this field was ingested). Winner is still {money.winner_agent || "unknown"}.
                        </td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </section>
      )}

      {view === "cost" && (
        <section className="panel">
          <h2 className="panel-title">Cost, tokens, models</h2>
          {!cost || (cost.agents ?? []).length === 0 ? (
            <p className="empty-state">
              No model_call_completed events for this match. Cost appears when an agent reports
              usage or the LLM gateway meters the turn.
            </p>
          ) : (
            <>
              <section className="cards">
                <article className="card">
                  <p className="card-label">Total tokens</p>
                  <p className="card-value">{ktoks(cost.total_tokens)}</p>
                </article>
                <article className="card">
                  <p className="card-label">Total spend</p>
                  <p className="card-value">{usd(cost.total_cost_usd)}</p>
                </article>
                <article className="card">
                  <p className="card-label">Model rows</p>
                  <p className="card-value">{cost.agents.length}</p>
                </article>
              </section>
              <div className="cost-bars">
                {cost.agents.map((a) => {
                  const max = Math.max(...cost.agents.map((x) => x.cost_usd), 0.0001);
                  const w = Math.max(4, (a.cost_usd / max) * 100);
                  return (
                    <div className="cost-bar-row" key={`${a.agent_id}-${a.model}-${a.meter_source}`}>
                      <div className="cost-bar-label mono">
                        {a.agent_id} · {a.provider}/{a.model}
                        {a.meter_source ? ` · ${a.meter_source}` : ""}
                      </div>
                      <div className="cost-bar-track">
                        <span className="cost-bar-fill" style={{ width: `${w}%` }} />
                      </div>
                      <div className="cost-bar-amt">{usd(a.cost_usd)}</div>
                    </div>
                  );
                })}
              </div>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Agent</th>
                      <th>Provider</th>
                      <th>Model</th>
                      <th>Meter</th>
                      <th>Calls</th>
                      <th>Prompt</th>
                      <th>Completion</th>
                      <th>Reasoning</th>
                      <th>Total tokens</th>
                      <th>USD</th>
                      <th>Avg latency</th>
                    </tr>
                  </thead>
                  <tbody>
                    {cost.agents.map((a) => (
                      <tr key={`${a.agent_id}-${a.model}-${a.meter_source}`}>
                        <td className="mono">
                          <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(a.agent_id)}`}>
                            {a.agent_id || "—"}
                          </Link>
                        </td>
                        <td>{a.provider}</td>
                        <td>{a.model}</td>
                        <td>{a.meter_source || "sdk"}</td>
                        <td>{a.calls}</td>
                        <td>{ktoks(a.prompt_tokens)}</td>
                        <td>{ktoks(a.completion_tokens)}</td>
                        <td>{ktoks(a.reasoning_tokens)}</td>
                        <td>{ktoks(a.total_tokens)}</td>
                        <td>{usd(a.cost_usd)}</td>
                        <td>{ms(a.avg_latency_ms)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}

          {agents.map((a) => {
            const b = a.benchmark ?? {};
            const log = b.decision_log ?? [];
            return (
              <div key={a.agent_id} style={{ marginTop: "1.4rem" }}>
                <h3 className="panel-title">
                  <Link className="agent-link" href={`/benchmarks/${encodeURIComponent(a.agent_id)}`}>
                    {a.agent_id}
                  </Link>{" "}
                  <span className="muted">per-move trail</span>
                </h3>
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>Round</th>
                        <th>Action</th>
                        <th>Outcome</th>
                        <th>Latency</th>
                        <th>Tokens</th>
                        <th>Reasoning</th>
                      </tr>
                    </thead>
                    <tbody>
                      {log.length ? (
                        log.map((d, i) => (
                          <tr key={i}>
                            <td>{d.round ?? "—"}</td>
                            <td className="mono">{d.action || "—"}</td>
                            <td>
                              <span className={outcomeClass(d.outcome)}>{d.outcome || "—"}</span>
                            </td>
                            <td>{ms(d.latency_ms ?? 0)}</td>
                            <td>{d.usage?.total_tokens ? ktoks(d.usage.total_tokens) : <span className="muted">—</span>}</td>
                            <td>{d.rationale || <span className="muted">—</span>}</td>
                          </tr>
                        ))
                      ) : (
                        <tr>
                          <td colSpan={6} className="empty-state">
                            No per-move trail recorded for this agent.
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
              </div>
            );
          })}
        </section>
      )}
    </div>
  );
}
