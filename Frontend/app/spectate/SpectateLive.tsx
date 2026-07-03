"use client";

import { useEffect, useRef, useState } from "react";
import { watchUrl } from "@/lib/api";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";

// Real-time spectator feed. Connects to the SSE endpoint
// GET /v1/match/{id}/watch and renders live rounds, scores and commentary.
interface StreamEvent {
  seq?: number;
  type?: string;
  payload?: any;
  commentary?: string;
  dramatic?: boolean;
}

type ConnState = "connecting" | "live" | "closed" | "error";

interface LiveRound {
  round: number;
  a: number; // cumulative score A at this round
  b: number;
  winner: "AGENT_A" | "AGENT_B" | "SPLIT";
}

export function SpectateLive({ matchId }: { matchId: string }) {
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [state, setState] = useState<ConnState>("connecting");
  const [score, setScore] = useState<{ a: number; b: number; round?: number; total?: number } | null>(null);
  const [rounds, setRounds] = useState<LiveRound[]>([]);
  const esRef = useRef<EventSource | null>(null);
  const lastScores = useRef<{ a: number; b: number }>({ a: 0, b: 0 });

  useEffect(() => {
    if (!matchId) return;
    setState("connecting");
    setRounds([]);
    lastScores.current = { a: 0, b: 0 };
    let es: EventSource;
    try {
      es = new EventSource(watchUrl(matchId));
    } catch {
      setState("error");
      return;
    }
    esRef.current = es;

    const onMsg = (m: MessageEvent) => {
      try {
        const ev: StreamEvent = JSON.parse(m.data);
        setEvents((prev) => [ev, ...prev].slice(0, 12));
        const p = ev.payload ?? {};
        if (Array.isArray(p.scores)) {
          const a = Number(p.scores[0] ?? 0);
          const b = Number(p.scores[1] ?? 0);
          setState("live");
          setScore({ a, b, round: p.round, total: p.total_rounds });
          // Derive the per-round bid winner from the cumulative-score delta.
          if (p.round != null) {
            const prev = lastScores.current;
            const dA = a - prev.a;
            const dB = b - prev.b;
            const winner: LiveRound["winner"] =
              dA > dB ? "AGENT_A" : dB > dA ? "AGENT_B" : "SPLIT";
            lastScores.current = { a, b };
            setRounds((prevR) => {
              if (prevR[0]?.round === p.round) return prevR; // de-dupe repeats
              return [{ round: p.round, a, b, winner }, ...prevR].slice(0, 12);
            });
          }
        }
      } catch {
        /* ignore keep-alives / non-JSON frames */
      }
    };

    es.onopen = () => setState("live");
    es.onmessage = onMsg;
    // Named events (event: tick / round / commentary …) also route here.
    ["tick", "round", "commentary", "state", "result"].forEach((t) =>
      es.addEventListener(t, onMsg as EventListener),
    );
    es.onerror = () => setState((s) => (s === "live" ? "closed" : "error"));

    return () => es.close();
  }, [matchId]);

  const tone = state === "live" ? "teal" : state === "connecting" ? "amber" : "red";
  const label =
    state === "live" ? "LIVE STREAM" : state === "connecting" ? "CONNECTING…" : state === "closed" ? "STREAM ENDED" : "OFFLINE";

  return (
    <Panel glass className="mt-5 p-5">
      <div className="mb-4 flex items-center justify-between">
        <div>
          <SectionLabel className="text-primary">LIVE FEED</SectionLabel>
          <p className="mt-0.5 font-mono text-[11px] text-ink-faint">
            Real-time round results streamed as the match plays out
          </p>
        </div>
        <Pill tone={tone} dot>
          {label}
        </Pill>
      </div>

      {score && (
        <div className="mb-4 flex items-center justify-center gap-6 border-b border-border-soft pb-4 font-mono">
          <span className="text-2xl font-semibold tabular-nums text-primary">{score.a}</span>
          <span className="label-caps text-ink-faint">
            {score.round != null ? `ROUND ${score.round}/${score.total ?? "?"}` : "SCORE"}
          </span>
          <span className="text-2xl font-semibold tabular-nums text-secondary">{score.b}</span>
        </div>
      )}

      <div className="grid gap-5 md:grid-cols-[1.4fr_1fr]">
        <div>
          <SectionLabel className="mb-2 text-ink-faint">EVENT LOG</SectionLabel>
          {events.length === 0 ? (
            <p className="font-mono text-[12px] text-ink-faint">
              {state === "error"
                ? "No live stream available for this match (backend offline or match finished)."
                : "Waiting for the first event from the match worker…"}
            </p>
          ) : (
            <div className="space-y-1.5 font-mono text-[12px] leading-5">
              {events.map((ev, i) => (
                <p key={`${ev.seq ?? "e"}-${i}`} className={cx(ev.dramatic ? "text-secondary" : "text-ink-dim")}>
                  <span className="text-ink-faint">
                    [{ev.type ?? "evt"}
                    {ev.seq != null ? ` #${ev.seq}` : ""}]
                  </span>{" "}
                  {ev.commentary ?? summarize(ev.payload)}
                </p>
              ))}
            </div>
          )}
        </div>

        <div className="md:border-l md:border-border-soft md:pl-5">
          <SectionLabel className="mb-2 text-ink-faint">LIVE ROUNDS · SSE</SectionLabel>
          {rounds.length === 0 ? (
            <p className="font-mono text-[12px] text-ink-faint">
              Round results stream in here as the match plays out.
            </p>
          ) : (
            <div className="space-y-2 font-mono text-[12px]">
              {rounds.map((r) => (
                <div key={r.round} className="flex items-center justify-between border-b border-border-soft pb-1.5">
                  <span className="text-ink-faint">R{String(r.round).padStart(2, "0")}</span>
                  <span className="text-ink-dim">
                    {String(r.a).padStart(2, "0")} <span className="text-ink-faint">vs</span>{" "}
                    {String(r.b).padStart(2, "0")}
                  </span>
                  <span
                    className={cx(
                      r.winner === "AGENT_A"
                        ? "text-primary"
                        : r.winner === "AGENT_B"
                          ? "text-secondary"
                          : "text-tertiary",
                    )}
                  >
                    {r.winner === "SPLIT" ? "SPLIT" : r.winner.replace("AGENT_", "")}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </Panel>
  );
}

function summarize(payload: any): string {
  if (!payload || typeof payload !== "object") return String(payload ?? "");
  if (payload.round != null && Array.isArray(payload.scores)) {
    return `round ${payload.round} · scores ${payload.scores.join(" – ")}`;
  }
  return JSON.stringify(payload);
}
