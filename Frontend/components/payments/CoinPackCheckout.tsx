"use client";

import { useEffect, useState } from "react";
import { Panel, SectionLabel, cx } from "@/components/ui";
import { fmt, type CoinPack } from "@/lib/mock";
import { fetchDepositQuote, topup, type DepositQuote } from "@/lib/api";
import { getSession } from "@/lib/session";
import { FeeRow } from "./FeeRow";
import { StripeBadge } from "./StripeBadge";

/** Stripe Checkout coin purchase — fee breakdown before redirect. */
export function CoinPackCheckout({
  packs,
  compact = false,
}: {
  packs: CoinPack[];
  compact?: boolean;
}) {
  const [selected, setSelected] = useState<CoinPack | null>(null);
  const [quote, setQuote] = useState<DepositQuote | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!selected) {
      setQuote(null);
      return;
    }
    fetchDepositQuote(getSession(), selected.key, "card")
      .then(setQuote)
      .catch(() => setQuote(null));
  }, [selected]);

  async function checkout() {
    if (!selected) return;
    const session = getSession();
    if (!session.dashboardToken) {
      setErr("Sign in to purchase coins.");
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const res = await topup(session, selected.key, session.agentId ?? "", "card");
      if (res.checkout_url) window.location.href = res.checkout_url;
      else {
        setErr("No checkout URL returned.");
        setBusy(false);
      }
    } catch (e) {
      setErr("✕ " + ((e as Error)?.message ?? "Checkout failed."));
      setBusy(false);
    }
  }

  return (
    <Panel className={compact ? "p-5" : "p-6"}>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <SectionLabel>BUY COINS</SectionLabel>
        <StripeBadge />
      </div>
      <p className="mb-4 font-mono text-[11px] leading-5 text-ink-faint">
        Secure checkout via Stripe. You receive the full coin amount; processing fees are added on top
        and never deducted from your balance.
      </p>

      <div className={cx("grid gap-3", compact ? "grid-cols-2" : "grid-cols-2 sm:grid-cols-4")}>
        {packs.map((p) => (
          <button
            key={p.key}
            type="button"
            onClick={() => setSelected(p)}
            disabled={busy}
            className={cx(
              "relative rounded-lg border p-4 text-left transition hover:border-primary/50 disabled:opacity-60",
              selected?.key === p.key
                ? "border-primary bg-primary-container/10 shadow-glow-teal"
                : "border-border-strong bg-bg-deep/40",
              p.popular && selected?.key !== p.key && "border-secondary/40",
            )}
          >
            {p.popular && (
              <span className="absolute -top-2 right-3 rounded-full bg-secondary px-2 py-0.5 font-mono text-[9px] font-bold uppercase tracking-caps text-on-secondary">
                Popular
              </span>
            )}
            <div className="label-caps">{p.label}</div>
            <div className="mt-2 font-mono text-lg font-semibold text-ink-primary">
              {fmt(p.coins)} <span className="text-xs text-ink-faint">CRD</span>
            </div>
            <div className={cx("mt-1 font-mono text-sm", p.popular ? "text-secondary" : "text-primary")}>
              ${p.priceUsd.toFixed(2)}
            </div>
          </button>
        ))}
      </div>

      {quote && selected && (
        <div className="mt-5 space-y-2 border-t border-border-soft pt-4">
          <FeeRow
            label="Coins purchased"
            value={`${fmt(quote.coins)} CRD ($${(quote.pack_cents / 100).toFixed(2)})`}
          />
          <FeeRow
            label="Stripe processing fee"
            value={`$${(quote.processing_fee_cents / 100).toFixed(2)}`}
            tone="text-ink-dim"
          />
          <FeeRow label="Total charged" value={`$${(quote.total_cents / 100).toFixed(2)}`} tone="text-primary" />
          <button
            type="button"
            onClick={checkout}
            disabled={busy}
            className="btn-primary mt-3 w-full disabled:opacity-50"
          >
            {busy ? "Opening Stripe checkout…" : "Pay with Stripe"}
          </button>
        </div>
      )}

      {err && <p className="mt-3 font-mono text-[11px] text-status-error">{err}</p>}
    </Panel>
  );
}
