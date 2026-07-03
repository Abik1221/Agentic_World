"use client";

import { useEffect, useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, Stat, cx } from "@/components/ui";
import { fmt } from "@/lib/mock";
import {
  fetchWithdrawable,
  fetchWithdrawal,
  fileDispute,
  type Withdrawal,
  type WithdrawQuote,
} from "@/lib/api";
import { getSession } from "@/lib/session";

// GET /v1/wallet/withdrawable, GET /v1/withdrawals/{id}, POST /v1/disputes (user).
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
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <SectionLabel className="mb-3 text-primary">CASH OUT</SectionLabel>
        <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Withdrawals &amp; disputes</h1>
        <p className="mt-2 max-w-xl text-ink-dim">
          Net winnings cash out through Stripe Connect after anti-fraud review and admin approval.
        </p>

        <div className="mt-8 grid gap-5 lg:grid-cols-2">
          {/* Withdrawable + quote */}
          <Panel glass className="p-7">
            <SectionLabel className="text-secondary">WITHDRAWABLE</SectionLabel>
            <div className="mt-3 font-mono text-4xl font-semibold tabular-nums text-ink-primary">
              {loading ? "…" : fmt(coins)} <span className="text-base text-ink-faint">CRD</span>
            </div>
            {quote ? (
              <div className="mt-5 space-y-2 border-t border-border-soft pt-4 font-mono text-sm">
                <Row k="Gross" v={`$${(quote.gross_cents / 100).toFixed(2)}`} />
                <Row k="Platform fee" v={`-${fmt(quote.platform_fee_coins)} CRD`} tone="text-status-error" />
                <Row k="Stripe fee" v={`-$${(quote.stripe_fee_cents / 100).toFixed(2)}`} tone="text-status-error" />
                <Row k="You receive" v={`$${(quote.net_cents / 100).toFixed(2)}`} tone="text-primary" />
              </div>
            ) : (
              <p className="mt-4 font-mono text-[12px] text-ink-faint">
                {loading ? "Loading quote…" : "Connect a session to see your payout quote."}
              </p>
            )}
            <p className="mt-5 font-mono text-[11px] text-ink-faint">
              File a withdrawal from the <a href="/guardrails" className="text-primary hover:underline">Guardrails</a> page.
            </p>
          </Panel>

          <div className="space-y-5">
            <LookupWithdrawal />
            <DisputeForm />
          </div>
        </div>
      </div>
      <Footer />
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
    setWd(null);
    const r = await fetchWithdrawal(getSession(), id.trim());
    if (r) setWd(r);
    else setErr("Not found (or not your withdrawal).");
    setBusy(false);
  }

  return (
    <Panel className="p-6">
      <SectionLabel className="mb-3">TRACK A WITHDRAWAL</SectionLabel>
      <div className="flex gap-2">
        <input className="input flex-1" placeholder="wd_… id" value={id} onChange={(e) => setId(e.target.value)} />
        <button onClick={go} disabled={busy} className="btn-neutral disabled:opacity-50">{busy ? "…" : "Track"}</button>
      </div>
      {err && <p className="mt-2 font-mono text-[11px] text-status-error">{err}</p>}
      {wd && (
        <div className="mt-4 grid grid-cols-2 gap-3 border-t border-border-soft pt-4">
          <Stat label="STATUS" value={wd.status.toUpperCase()} tone="amber" />
          <Stat label="NET" value={`$${(wd.net_cents / 100).toFixed(2)}`} tone="teal" />
          <Stat label="COINS" value={fmt(wd.coins)} />
          <Stat label="FEE" value={`${fmt(wd.fee_coins)} CRD`} />
        </div>
      )}
    </Panel>
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
      const r = await fileDispute(session, {
        match: match.trim(),
        agent: session.agentId ?? "",
        kind: "review",
        detail: detail.trim(),
      });
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
    <Panel className="p-6">
      <SectionLabel className="mb-3">FLAG A MATCH</SectionLabel>
      <div className="space-y-3">
        <input className="input" placeholder="match id" value={match} onChange={(e) => setMatch(e.target.value)} />
        <textarea
          className="input min-h-[72px] resize-none"
          placeholder="what looked wrong?"
          value={detail}
          onChange={(e) => setDetail(e.target.value)}
        />
        <button onClick={submit} disabled={busy} className="btn-primary w-full disabled:opacity-50">
          {busy ? "Filing…" : "File dispute"}
        </button>
        {msg && (
          <p className={cx("font-mono text-[11px]", msg.startsWith("✓") ? "text-primary" : "text-status-error")}>
            {msg}
          </p>
        )}
      </div>
    </Panel>
  );
}

function Row({ k, v, tone }: { k: string; v: string; tone?: string }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-ink-dim">{k}</span>
      <span className={tone ?? "text-ink-primary"}>{v}</span>
    </div>
  );
}
