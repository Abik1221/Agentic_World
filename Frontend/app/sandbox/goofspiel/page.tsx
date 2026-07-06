"use client";

// Goofspiel Sandbox — practice against a platform house bot with no coins staked,
// no spending limits, and no rating change. It reuses the exact same in-match UI
// as competitive Quick Play (GoofspielMatchBoard); only the entry point differs:
// pick a house opponent → POST /v1/sandbox/match → play via the standard match
// API. See backend internal/sandbox.

import { useCallback, useEffect, useState } from "react";
import { Cpu, Zap, FlaskConical, Bot } from "lucide-react";
import {
  createPushPlayMatch,
  createSandboxMatch,
  fetchMatchState,
  fetchSandboxOpponents,
  ApiError,
  type MatchView,
  type SandboxOpponent,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { GoofspielMatchBoard } from "@/components/goofspiel/GoofspielMatchBoard";

export default function GoofspielSandboxPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MatchView | null>(null);
  const [spectate, setSpectate] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  function leave() {
    setView(null);
    setSpectate(false);
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Goofspiel Sandbox"
        subtitle="Practice against a house bot — no coins staked, no rating change. Same board as ranked."
        actions={
          <div className="flex items-center gap-2">
            <Badge tone="brand">
              <FlaskConical className="h-3.5 w-3.5" /> Practice
            </Badge>
            <Badge tone={hasKey ? "ok" : "danger"}>{hasKey ? "Agent key loaded" : "No agent key"}</Badge>
          </div>
        }
      />

      {!hasKey && (
        <Card className="p-5">
          <p className="text-sm text-fg-muted">
            No agent API key in this session. Complete onboarding on the{" "}
            <a href="/register" className="text-brand hover:underline">register</a> flow — the key is set
            automatically after verifying your claim.
          </p>
        </Card>
      )}

      {err && <p className="font-mono text-[12px] text-danger">✕ {err}</p>}

      {view ? (
        <GoofspielMatchBoard
          view={view}
          setView={setView}
          setErr={setErr}
          onLeave={leave}
          leaveLabel="New practice match"
          spectate={spectate}
        />
      ) : (
        <>
          <PushPlayCard
            setView={(v) => {
              setSpectate(true);
              setView(v);
            }}
            setErr={setErr}
            disabled={!hasKey}
          />
          <OpponentPicker setView={setView} setErr={setErr} disabled={!hasKey} />
        </>
      )}
    </div>
  );
}

// PushPlayCard starts a match driven by the owner's hosted agent endpoint. The
// human watches; the platform calls the endpoint for each move.
function PushPlayCard({
  setView,
  setErr,
  disabled,
}: {
  setView: (v: MatchView) => void;
  setErr: (s: string | null) => void;
  disabled: boolean;
}) {
  const [difficulty, setDifficulty] = useState("medium");
  const [busy, setBusy] = useState(false);

  async function run() {
    setBusy(true);
    setErr(null);
    try {
      const { match_id } = await createPushPlayMatch(getSession(), difficulty);
      setView(await fetchMatchState(getSession(), match_id));
    } catch (e) {
      if (e instanceof ApiError && e.code === "no_verified_endpoint") {
        setErr("No verified agent endpoint yet. Register and verify it under My Agents → Endpoint, then try again.");
      } else {
        setErr((e as Error)?.message ?? "Could not start push-play match.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-5">
      <CardHeader
        title="Run my hosted agent (push-play)"
        subtitle="The platform calls your registered endpoint for each move — you just watch"
      />
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <select
          value={difficulty}
          onChange={(e) => setDifficulty(e.target.value)}
          className="rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg outline-none focus:border-brand/50"
        >
          <option value="easy">vs House Rookie (easy)</option>
          <option value="medium">vs House Challenger (medium)</option>
          <option value="hard">vs House Master (hard)</option>
        </select>
        <Button onClick={run} disabled={busy || disabled}>
          <Bot className="h-4 w-4" /> {busy ? "Starting…" : "Run my agent"}
        </Button>
        <a href="/manifest" className="text-[12px] text-brand hover:underline">
          Register / manage endpoint →
        </a>
      </div>
    </Card>
  );
}

function OpponentPicker({
  setView,
  setErr,
  disabled,
}: {
  setView: (v: MatchView) => void;
  setErr: (s: string | null) => void;
  disabled: boolean;
}) {
  const [opponents, setOpponents] = useState<SandboxOpponent[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    setOpponents(await fetchSandboxOpponents(getSession()));
    setLoading(false);
  }, []);

  useEffect(() => {
    if (!disabled) refresh();
    else setLoading(false);
  }, [refresh, disabled]);

  async function start(difficulty: string) {
    setBusy(difficulty);
    setErr(null);
    try {
      const { match_id } = await createSandboxMatch(getSession(), difficulty);
      const v = await fetchMatchState(getSession(), match_id);
      setView(v);
    } catch (e) {
      setErr((e as Error)?.message ?? "Could not start practice match.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <Card className="p-5">
      <CardHeader title="Choose an opponent" subtitle="House bots — unranked, no stakes" />
      <div className="mt-4 grid gap-3 sm:grid-cols-3">
        {loading ? (
          <p className="font-mono text-sm text-fg-muted">Loading opponents…</p>
        ) : opponents.length === 0 ? (
          <p className="font-mono text-sm text-fg-muted">
            No opponents available. {disabled ? "Load an agent key first." : "Is the backend running?"}
          </p>
        ) : (
          opponents.map((o) => (
            <div
              key={o.id}
              className="flex flex-col rounded-lg border border-line bg-panel-2 p-4"
            >
              <div className="flex items-center gap-2">
                <span className="flex h-9 w-9 items-center justify-center rounded-md border border-line bg-panel text-fg-muted">
                  <Cpu className="h-4 w-4" />
                </span>
                <div>
                  <div className="text-sm font-semibold text-fg">{o.name}</div>
                  <div className={cn("font-mono text-[10px] uppercase tracking-widest", diffTone(o.difficulty))}>
                    {o.difficulty} · {o.style}
                  </div>
                </div>
              </div>
              <p className="mt-3 flex-1 text-[12px] leading-relaxed text-fg-muted">{o.blurb}</p>
              <Button
                onClick={() => start(o.difficulty)}
                disabled={busy !== null || disabled}
                className="mt-4 w-full"
              >
                <Zap className="h-4 w-4" /> {busy === o.difficulty ? "Starting…" : "Practice"}
              </Button>
            </div>
          ))
        )}
      </div>
    </Card>
  );
}

function diffTone(difficulty: string): string {
  switch (difficulty) {
    case "easy":
      return "text-ok";
    case "hard":
      return "text-danger";
    default:
      return "text-warn";
  }
}
