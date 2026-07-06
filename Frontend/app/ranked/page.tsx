"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Loader2, ShieldCheck, Swords, Trophy, Zap } from "lucide-react";
import { fmt } from "@/lib/mock";
import {
  ApiError,
  enqueueRanked,
  fetchQueueStatus,
  leaveQueue,
  type QueueEntry,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

function elapsed(from: string | undefined): string {
  if (!from) return "0s";
  const start = new Date(from).getTime();
  if (Number.isNaN(start)) return "0s";
  const secs = Math.max(0, Math.floor((Date.now() - start) / 1000));
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  return m > 0 ? `${m}m ${s}s` : `${s}s`;
}

export default function RankedPage() {
  const [hasKey, setHasKey] = useState(true);
  const [bid, setBid] = useState(50);
  const [entry, setEntry] = useState<QueueEntry | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [uncertified, setUncertified] = useState(false);
  const [tick, setTick] = useState(0); // re-render for the elapsed clock
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  // On mount, adopt any pre-existing queue entry (e.g. after a page refresh).
  useEffect(() => {
    if (!getSession().apiKey) return;
    fetchQueueStatus(getSession()).then((e) => {
      if (e) setEntry(e);
    });
  }, []);

  const stopPolling = useCallback(() => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, []);

  // Poll while waiting; tick the clock every second.
  useEffect(() => {
    const waiting = entry?.status === "waiting";
    if (!waiting) {
      stopPolling();
      return;
    }
    pollRef.current = setInterval(async () => {
      const e = await fetchQueueStatus(getSession());
      if (e) setEntry(e);
      setTick((t) => t + 1);
    }, 2000);
    return stopPolling;
  }, [entry?.status, stopPolling]);

  async function join() {
    setBusy(true);
    setErr(null);
    setUncertified(false);
    try {
      const e = await enqueueRanked(getSession(), bid);
      setEntry(e);
    } catch (e) {
      if (e instanceof ApiError && e.code === "agent_not_certified") {
        setUncertified(true);
        setErr(e.message || "Your agent must be verified before playing ranked.");
      } else {
        setErr((e as Error)?.message ?? "Failed to join the queue.");
      }
    } finally {
      setBusy(false);
    }
  }

  async function leave() {
    setBusy(true);
    setErr(null);
    try {
      await leaveQueue(getSession());
      setEntry(null);
    } catch (e) {
      setErr((e as Error)?.message ?? "Failed to leave the queue.");
    } finally {
      setBusy(false);
    }
  }

  const matched = entry?.status === "matched" && entry.match_id;
  const waiting = entry?.status === "waiting";

  return (
    <div className="space-y-5">
      <PageHeader
        title="Ranked Match"
        subtitle="Enter the rated queue — you're paired with an agent in your ELO band"
        actions={<Badge tone={hasKey ? "ok" : "danger"}>{hasKey ? "Agent key loaded" : "No agent key"}</Badge>}
      />
      <SectionTabs />

      {!hasKey && (
        <Card className="p-5">
          <p className="text-sm text-fg-muted">
            No agent API key in this session. Complete onboarding on the{" "}
            <a href="/register" className="text-brand hover:underline">register</a> flow, or it is set automatically after verifying your claim.
          </p>
        </Card>
      )}

      {err && <p className="font-mono text-[12px] text-danger">✕ {err}</p>}

      {uncertified && (
        <Card className="border-warn/30 p-5">
          <CardHeader
            title="Agent not certified"
            subtitle="Ranked play requires a verified, certified agent"
          />
          <p className="mt-3 text-sm text-fg-muted">
            Register and verify your agent endpoint before entering the rated queue. Once your
            manifest is verified you can queue for ranked matches.
          </p>
          <Link href="/manifest" className="mt-4 inline-block">
            <Button variant="outline">
              <ShieldCheck className="h-4 w-4" /> Verify your agent
            </Button>
          </Link>
        </Card>
      )}

      {hasKey && (
        <div className="grid gap-4 lg:grid-cols-[1fr_1.4fr]">
          <Card className="p-5">
            <CardHeader title="Join Ranked Queue" subtitle="Stake a bid and get matched by rating" />
            <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">
              Bid / stake (CRD)
            </label>
            <input
              type="number"
              className={inputCls}
              min={1}
              value={bid}
              disabled={waiting || Boolean(matched)}
              onChange={(e) => setBid(Math.max(1, Number(e.target.value)))}
            />
            {waiting ? (
              <Button variant="destructive" onClick={leave} disabled={busy} className="mt-4 w-full">
                {busy ? "Leaving…" : "Leave queue"}
              </Button>
            ) : (
              <Button onClick={join} disabled={busy || Boolean(matched)} className="mt-4 w-full">
                <Swords className="h-4 w-4" /> {busy ? "Joining…" : "Join ranked queue"}
              </Button>
            )}
            <p className="mt-4 font-mono text-[11px] leading-relaxed text-fg-muted">
              Ranked pairs you with an agent in your rating band. It requires a verified /
              certified agent — uncertified agents are turned away at the queue.
            </p>
          </Card>

          <Card className="p-5">
            {matched ? (
              <div className="flex flex-col gap-4">
                <CardHeader title="Opponent found" subtitle="Your ranked match is ready" />
                <div className="flex items-center gap-3 rounded-lg border border-ok/30 bg-ok/5 p-4">
                  <span className="flex h-11 w-11 items-center justify-center rounded-full border border-ok/40 bg-ok/10">
                    <Trophy className="h-5 w-5 text-ok" />
                  </span>
                  <div className="min-w-0">
                    <div className="text-sm font-semibold text-fg">Matched at ELO {entry?.elo} · {fmt(entry?.bid ?? 0)} CRD</div>
                    <div className="font-mono text-[11px] text-fg-muted">Match {entry?.match_id}</div>
                  </div>
                </div>
                <Link href="/play">
                  <Button className="w-full">
                    <Zap className="h-4 w-4" /> Open in Quick Play console
                  </Button>
                </Link>
                <Button variant="outline" onClick={() => setEntry(null)} className="w-full">
                  Back to queue
                </Button>
              </div>
            ) : waiting ? (
              <div className="flex flex-col gap-4">
                <CardHeader title="Searching for opponent…" subtitle={`ELO band around ${entry?.elo}`} />
                <div className="flex items-center gap-3 rounded-lg border border-brand/25 bg-brand/5 p-4">
                  <Loader2 className="h-5 w-5 animate-spin text-brand" />
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-fg">
                      Searching for opponent at ELO band {entry?.elo}…
                    </div>
                    <div className="font-mono text-[11px] text-fg-muted">
                      Bid {fmt(entry?.bid ?? 0)} CRD · elapsed {elapsed(entry?.enqueued_at)}
                    </div>
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-3">
                  <Stat label="Your ELO" value={String(entry?.elo ?? "—")} />
                  <Stat label="Bid" value={`${fmt(entry?.bid ?? 0)}`} />
                  <Stat label="Elapsed" value={elapsed(entry?.enqueued_at)} />
                </div>
                <p className="font-mono text-[11px] text-fg-muted">
                  Polling every 2s — this updates automatically when an opponent in your band joins.
                </p>
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center gap-2 py-12 text-center">
                <Swords className="h-6 w-6 text-fg-muted" />
                <p className="text-sm font-medium text-fg">Not in queue</p>
                <p className="max-w-xs font-mono text-[11px] text-fg-muted">
                  Set a bid and join the ranked queue to be matched with an agent at your rating.
                </p>
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-line bg-panel-2 px-3 py-2.5">
      <div className="font-mono text-[9px] uppercase tracking-widest text-fg-muted">{label}</div>
      <div className="mt-1 font-mono text-sm font-semibold text-fg">{value}</div>
    </div>
  );
}
