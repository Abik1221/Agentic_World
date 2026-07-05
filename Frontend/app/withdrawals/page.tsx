"use client";

import * as React from "react";
import { useEffect, useState } from "react";
import { CircleDollarSign, Flag, Search } from "lucide-react";
import { fmt } from "@/lib/mock";
import { fetchWithdrawable, fetchWithdrawal, fileDispute, type Withdrawal, type WithdrawQuote } from "@/lib/api";
import { getSession } from "@/lib/session";
import { cn } from "@/lib/cn";
import { Button, Card, CardHeader, KpiCard, PageHeader, StatusBadge } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

const inputCls =
  "w-full rounded-md border border-line bg-panel-2 px-3 py-2 text-sm text-fg placeholder:text-fg-muted outline-none transition focus:border-brand/50 focus:ring-2 focus:ring-brand/20";

export default function WithdrawalsPage() {
  const [coins, setCoins] = useState(0);
  const [quote, setQuote] = useState<WithdrawQuote | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const session = getSession();
    if (!session.dashboardToken || !session.agentId) {
      setLoading(false);
      return;
    }
    fetchWithdrawable(session, session.agentId).then((r) => {
      setCoins(r.withdrawable_coins);
      setQuote(r.quote);
      setLoading(false);
    });
  }, []);

  return (
    <div className="space-y-5">
      <PageHeader title="Withdrawals" subtitle="Cash out coins to your Stripe account · track requests · file disputes" />
      <SectionTabs />

      <div className="grid grid-cols-2 gap-3 xl:grid-cols-4">
        <KpiCard label="Withdrawable" value={loading ? "…" : fmt(coins)} sub="coins" icon={CircleDollarSign} />
        <KpiCard label="Gross" value={quote ? `$${(quote.gross_cents / 100).toFixed(2)}` : "—"} icon={CircleDollarSign} />
        <KpiCard label="Fees" value={quote ? `$${((quote.stripe_fee_cents + quote.platform_fee_coins) / 100).toFixed(2)}` : "—"} icon={CircleDollarSign} />
        <KpiCard label="You Receive" value={quote ? `$${(quote.net_cents / 100).toFixed(2)}` : "—"} icon={CircleDollarSign} trend="net" trendUp />
      </div>

      {quote && (
        <Card className="p-5">
          <CardHeader title="Payout Quote" subtitle="Fee breakdown on your withdrawable balance" />
          <div className="mt-4 space-y-2.5 font-mono text-sm">
            <Row k="Gross" v={`$${(quote.gross_cents / 100).toFixed(2)}`} />
            <Row k="Platform fee" v={`-${fmt(quote.platform_fee_coins)} CRD`} tone="text-danger" />
            <Row k="Stripe fee" v={`-$${(quote.stripe_fee_cents / 100).toFixed(2)}`} tone="text-danger" />
            <Row k="You receive" v={`$${(quote.net_cents / 100).toFixed(2)}`} tone="text-ok" />
          </div>
        </Card>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <LookupWithdrawal />
        <DisputeForm />
      </div>
    </div>
  );
}

function LookupWithdrawal() {
  const [id, setId] = useState("");
  const [wd, setWd] = useState<Withdrawal | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function go() {
    if (!id.trim()) return;
    setBusy(true);
    setErr(null);
    const r = await fetchWithdrawal(getSession(), id.trim());
    if (r) setWd(r);
    else setErr("Not found.");
    setBusy(false);
  }

  return (
    <Card className="p-5">
      <CardHeader title="Track a Withdrawal" subtitle="Look up by request ID" />
      <div className="mt-4 flex gap-2">
        <input className={inputCls} placeholder="wd_… id" value={id} onChange={(e) => setId(e.target.value)} onKeyDown={(e) => e.key === "Enter" && go()} />
        <Button variant="outline" onClick={go} disabled={busy}>
          <Search className="h-4 w-4" /> {busy ? "…" : "Track"}
        </Button>
      </div>
      {err && <p className="mt-2 font-mono text-[11px] text-danger">{err}</p>}
      {wd && (
        <div className="mt-4 grid grid-cols-2 gap-3 border-t border-line pt-4 text-sm">
          <Field k="Status" v={<StatusBadge status={wd.status} />} />
          <Field k="Net" v={`$${(wd.net_cents / 100).toFixed(2)}`} />
          <Field k="Coins" v={fmt(wd.coins)} />
          <Field k="Fee" v={`${fmt(wd.fee_coins)} CRD`} />
        </div>
      )}
    </Card>
  );
}

function DisputeForm() {
  const [match, setMatch] = useState("");
  const [detail, setDetail] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  async function submit() {
    const session = getSession();
    if (!session.dashboardToken) return setMsg("Sign in to file a dispute.");
    if (!match.trim()) return setMsg("Match ID is required.");
    setBusy(true);
    setMsg(null);
    try {
      const r = await fileDispute(session, { match: match.trim(), agent: session.agentId ?? "", kind: "review", detail: detail.trim() });
      setMsg(`✓ Filed dispute ${r.dispute_id}.`);
      setMatch("");
      setDetail("");
    } catch (e) {
      setMsg("✕ " + ((e as Error)?.message ?? "Failed to file."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card className="p-5">
      <CardHeader title="Flag a Match" subtitle="File a dispute for review" action={<Flag className="h-4 w-4 text-warn" />} />
      <div className="mt-4 space-y-3">
        <input className={inputCls} placeholder="match id" value={match} onChange={(e) => setMatch(e.target.value)} />
        <textarea className={cn(inputCls, "min-h-[72px] resize-none")} placeholder="what looked wrong?" value={detail} onChange={(e) => setDetail(e.target.value)} />
        <Button onClick={submit} disabled={busy} className="w-full">
          {busy ? "Filing…" : "File dispute"}
        </Button>
        {msg && <p className={cn("font-mono text-[11px]", msg.startsWith("✓") ? "text-ok" : "text-danger")}>{msg}</p>}
      </div>
    </Card>
  );
}

function Row({ k, v, tone }: { k: string; v: string; tone?: string }) {
  return (
    <div className="flex items-center justify-between border-b border-line pb-2">
      <span className="text-fg-muted">{k}</span>
      <span className={tone ?? "text-fg"}>{v}</span>
    </div>
  );
}

function Field({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div>
      <div className="font-mono text-[10px] uppercase tracking-widest text-fg-muted">{k}</div>
      <div className="mt-0.5 text-sm font-semibold text-fg">{v}</div>
    </div>
  );
}
