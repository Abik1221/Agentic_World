"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { Panel, SectionLabel } from "@/components/ui";
import { Coin } from "@/components/icons";
import { fmt } from "@/lib/mock";
import {
  fetchWithdrawable,
  payoutsOnboard,
  requestWithdrawal,
  type WithdrawQuote,
  type Withdrawal,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { FeeRow } from "./FeeRow";
import { StripeBadge } from "./StripeBadge";

/** Stripe Connect cash-out with live fee quote from the backend. */
export function CashOutPanel() {
  const [coins, setCoins] = useState(0);
  const [quote, setQuote] = useState<WithdrawQuote | null>(null);
  const [busy, setBusy] = useState<"load" | "file" | "onboard" | null>("load");
  const [confirmed, setConfirmed] = useState(false);
  const [result, setResult] = useState<Withdrawal | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  useEffect(() => {
    const session = getSession();
    if (!session.dashboardToken || !session.agentId) {
      setBusy(null);
      return;
    }
    fetchWithdrawable(session, session.agentId)
      .then((r) => {
        setCoins(r.withdrawable_coins);
        setQuote(r.quote);
      })
      .finally(() => setBusy(null));
  }, []);

  async function file() {
    const session = getSession();
    if (!session.agentId) return setMsg("Connect a session first.");
    if (coins <= 0) return setMsg("Nothing withdrawable yet — win tournaments to unlock cash-out.");
    if (!confirmed) {
      setConfirmed(true);
      setMsg("Review the breakdown above, then confirm again to file.");
      return;
    }
    setBusy("file");
    setMsg(null);
    try {
      const wd = await requestWithdrawal(session, session.agentId, coins);
      setResult(wd);
      setMsg(`✓ Request filed · ${wd.withdrawal_id}`);
      setConfirmed(false);
    } catch (e) {
      setMsg("✕ " + ((e as Error)?.message ?? "Withdrawal failed."));
    } finally {
      setBusy(null);
    }
  }

  async function onboard() {
    setBusy("onboard");
    setMsg(null);
    try {
      const r = await payoutsOnboard(getSession());
      if (r.onboarding_url) window.location.href = r.onboarding_url;
    } catch (e) {
      setMsg("✕ " + ((e as Error)?.message ?? "Stripe Connect setup failed."));
      setBusy(null);
    }
  }

  return (
    <Panel glass className="p-6">
      <div className="mb-4 flex items-center justify-between">
        <SectionLabel className="text-primary">CASH OUT</SectionLabel>
        <span className="text-primary">
          <Coin width={18} height={18} />
        </span>
      </div>

      <div className="font-mono text-3xl font-semibold tabular-nums text-ink-primary">
        {busy === "load" ? "…" : fmt(coins)} <span className="text-base text-ink-faint">CRD</span>
      </div>
      <div className="label-caps mt-1">WITHDRAWABLE (NET WINNINGS)</div>

      {quote && coins > 0 ? (
        <div className="mt-5 space-y-2 border-t border-border-soft pt-4">
          <FeeRow label="Gross value" value={`$${(quote.gross_cents / 100).toFixed(2)}`} />
          <FeeRow
            label="Platform fee"
            value={`-${fmt(quote.platform_fee_coins)} CRD`}
            tone="text-status-error"
          />
          <FeeRow
            label="Stripe payout fee"
            value={`-$${(quote.stripe_fee_cents / 100).toFixed(2)}`}
            tone="text-status-error"
          />
          <FeeRow label="You receive" value={`$${(quote.net_cents / 100).toFixed(2)}`} tone="text-primary" />
        </div>
      ) : (
        <p className="mt-4 font-mono text-[11px] text-ink-faint">
          Only tournament winnings are withdrawable. Deposits and Arena Pass grants are for play.
        </p>
      )}

      <div className="mt-4">
        <StripeBadge className="w-full justify-center" />
      </div>

      <button
        type="button"
        onClick={file}
        disabled={busy !== null || coins <= 0}
        className="btn-primary mt-5 w-full disabled:cursor-not-allowed disabled:opacity-50"
      >
        {busy === "file"
          ? "Filing…"
          : confirmed
            ? "Confirm withdrawal"
            : "Review & file withdrawal"}
      </button>
      <button
        type="button"
        onClick={onboard}
        disabled={busy !== null}
        className="btn-ghost mt-2 w-full disabled:opacity-50"
      >
        {busy === "onboard" ? "Redirecting…" : "Set up Stripe payouts (KYC)"}
      </button>

      <p className="mt-2 text-center font-mono text-[11px] text-ink-faint">
        {msg ?? "Payouts via Stripe Connect · Admin-approved after clearing window"}
      </p>
      {result && (
        <p className="mt-1 text-center font-mono text-[11px] text-primary">
          Track on{" "}
          <Link href="/withdrawals" className="underline">
            withdrawals
          </Link>
        </p>
      )}
    </Panel>
  );
}
