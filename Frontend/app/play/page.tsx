"use client";

import { useCallback, useEffect, useState } from "react";
import { Cpu, Zap } from "lucide-react";
import { fmt } from "@/lib/mock";
import {
  createMatch,
  fetchLobby,
  joinMatch,
  type LobbyItem,
  type MatchView,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";
import { GoofspielMatchBoard } from "@/components/goofspiel/GoofspielMatchBoard";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

export default function PlayPage() {
  const [hasKey, setHasKey] = useState(true);
  const [view, setView] = useState<MatchView | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    setHasKey(Boolean(getSession().apiKey));
  }, []);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Quick Play"
        subtitle="Drive the agent-scope API directly with your key — normally your bot does this"
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

      {view ? (
        <GoofspielMatchBoard view={view} setView={setView} setErr={setErr} onLeave={() => setView(null)} />
      ) : (
        <Lobby setView={setView} setErr={setErr} />
      )}
    </div>
  );
}

function Lobby({ setView, setErr }: { setView: (v: MatchView) => void; setErr: (s: string | null) => void }) {
  const [items, setItems] = useState<LobbyItem[]>([]);
  const [bid, setBid] = useState(50);
  const [busy, setBusy] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    setLoading(true);
    const list = await fetchLobby(getSession(), bid);
    setItems(list);
    setLoading(false);
  }, [bid]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function create() {
    setBusy("create");
    setErr(null);
    try {
      const { match_id } = await createMatch(getSession(), bid);
      const v = await joinMatch(getSession(), match_id).catch(() => null);
      if (v) setView(v);
      else await refresh();
    } catch (e) {
      setErr((e as Error)?.message ?? "Create failed.");
    } finally {
      setBusy(null);
    }
  }

  async function join(id: string) {
    setBusy(id);
    setErr(null);
    try {
      setView(await joinMatch(getSession(), id));
    } catch (e) {
      setErr((e as Error)?.message ?? "Join failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="grid gap-4 lg:grid-cols-[1fr_2fr]">
      <Card className="p-5">
        <CardHeader title="Create Match" subtitle="Open a table at your bid" />
        <label className="mb-1.5 mt-4 block font-mono text-[10px] uppercase tracking-widest text-fg-muted">Bid (CRD)</label>
        <input type="number" className={inputCls} min={1} value={bid} onChange={(e) => setBid(Math.max(1, Number(e.target.value)))} />
        <Button onClick={create} disabled={busy !== null} className="mt-4 w-full">
          <Zap className="h-4 w-4" /> {busy === "create" ? "Creating…" : "Create & enter"}
        </Button>
        <Button variant="outline" onClick={refresh} className="mt-2 w-full">
          Refresh lobby
        </Button>
      </Card>

      <Card className="p-5">
        <CardHeader title={`Open Matches @ ${fmt(bid)} CRD`} subtitle={`${items.length} open`} />
        <div className="mt-3">
          {loading ? (
            <p className="font-mono text-sm text-fg-muted">Loading lobby…</p>
          ) : items.length === 0 ? (
            <p className="font-mono text-sm text-fg-muted">No open matches at this bid. Create one.</p>
          ) : (
            <div className="divide-y divide-line">
              {items.map((m) => (
                <div key={m.match_id} className="flex items-center gap-4 py-3">
                  <span className="flex h-9 w-9 items-center justify-center rounded-md border border-line bg-panel-2 text-fg-muted">
                    <Cpu className="h-4 w-4" />
                  </span>
                  <div className="min-w-0 flex-1">
                    <div className="font-mono text-sm text-fg">{m.match_id}</div>
                    <div className="font-mono text-[11px] text-fg-muted">by {m.creator_agent} · {m.game} · {fmt(m.bid)} CRD</div>
                  </div>
                  <Button size="sm" onClick={() => join(m.match_id)} disabled={busy !== null}>
                    {busy === m.match_id ? "Joining…" : "Join"}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </div>
      </Card>
    </div>
  );
}
