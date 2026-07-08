"use client";

import * as React from "react";
import { Loader2, CheckCircle2, AlertCircle, ArrowUpRight } from "lucide-react";
import { Modal } from "@/components/Modal";
import { Button, SectionLabel, cx } from "@/components/ui";
import {
  fetchWithdrawable,
  requestWithdrawal,
  fetchWithdrawal,
  ApiError,
  type WithdrawQuote,
  type Withdrawal,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { fmt } from "@/lib/mock";

const POLL_MS = 5000;
type Step = "amount" | "confirm" | "status" | "error";

// WithdrawModal cashes net winnings out to the user's linked Solana wallet: pick
// an amount → see the fee quote → confirm → track the request. The payout is
// escrow-held, admin-cleared, then broadcast + confirmed on-chain by the backend;
// this modal reflects that status as it advances.
export function WithdrawModal({
  open,
  onClose,
  onRequested,
}: {
  open: boolean;
  onClose: () => void;
  onRequested?: () => void;
}) {
  const [step, setStep] = React.useState<Step>("amount");
  const [maxCoins, setMaxCoins] = React.useState(0);
  const [coins, setCoins] = React.useState(0);
  const [quote, setQuote] = React.useState<WithdrawQuote | null>(null);
  const [wd, setWd] = React.useState<Withdrawal | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const usd = (cents: number) => `$${(cents / 100).toFixed(2)}`;

  React.useEffect(() => {
    if (!open) return;
    setStep("amount");
    setCoins(0);
    setQuote(null);
    setWd(null);
    setError(null);
    setBusy(false);
    const session = getSession();
    fetchWithdrawable(session, session.agentId).then((r) => {
      setMaxCoins(r.withdrawable_coins);
      setCoins(r.withdrawable_coins);
      setQuote(r.quote);
    });
  }, [open]);

  // Re-quote (debounced) when the amount changes.
  React.useEffect(() => {
    if (step !== "amount" || coins <= 0) return;
    const t = setTimeout(() => {
      fetchWithdrawable(getSession(), getSession().agentId, coins).then((r) => setQuote(r.quote));
    }, 350);
    return () => clearTimeout(t);
  }, [coins, step]);

  // Poll the request status until it reaches a terminal state.
  React.useEffect(() => {
    if (step !== "status" || !wd) return;
    let alive = true;
    const id = setInterval(async () => {
      const r = await fetchWithdrawal(getSession(), wd.withdrawal_id);
      if (!alive || !r) return;
      setWd(r);
    }, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [step, wd]);

  async function submit() {
    const session = getSession();
    if (!session.agentId) {
      setError("No agent found for this account.");
      setStep("error");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const r = await requestWithdrawal(session, session.agentId, coins);
      setWd(r);
      setStep("status");
      onRequested?.();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.status === 404
            ? "Withdrawals aren't available yet."
            : err.message
          : "Could not file the withdrawal. Please try again.",
      );
      setStep("error");
    } finally {
      setBusy(false);
    }
  }

  const terminal = wd?.status === "paid" || wd?.status === "rejected" || wd?.status === "failed";
  const paid = wd?.status === "paid";

  return (
    <Modal open={open} onClose={onClose} eyebrow="SOLANA · USDC" title="Withdraw USDC" size="md">
      {step === "amount" && (
        <div className="space-y-5">
          <p className="text-ink-dim">
            Cash out your net winnings to your linked Solana wallet as USDC. Only winnings are
            withdrawable (deposited credits are play-only).
          </p>
          <div>
            <div className="mb-1.5 flex items-center justify-between">
              <SectionLabel>Amount (credits)</SectionLabel>
              <button className="font-mono text-[11px] text-primary hover:text-tertiary" onClick={() => setCoins(maxCoins)}>
                Max {fmt(maxCoins)}
              </button>
            </div>
            <input
              type="number"
              min={0}
              max={maxCoins}
              value={coins}
              onChange={(e) => setCoins(Math.min(maxCoins, Math.max(0, Math.floor(Number(e.target.value)))))}
              className="w-full rounded-lg border border-border-soft bg-surface-high/40 px-3 py-2 font-mono text-sm text-ink-primary outline-none"
            />
          </div>

          <div className="space-y-2 rounded-lg border border-border-soft bg-surface-high/30 p-4 font-mono text-[13px]">
            <QuoteRow k="Gross" v={quote ? usd(quote.gross_cents) : "—"} />
            <QuoteRow k="Platform fee" v={quote ? `−${fmt(quote.platform_fee_coins)} cr` : "—"} tone="text-status-error" />
            <QuoteRow k="Network fee" v={quote ? `−${usd(quote.stripe_fee_cents)}` : "—"} tone="text-status-error" />
            <div className="mt-1 border-t border-border-soft pt-2">
              <QuoteRow k="You receive" v={quote ? `${usd(quote.net_cents)} USDC` : "—"} tone="text-primary" bold />
            </div>
          </div>

          {error && <ErrorLine>{error}</ErrorLine>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button
              onClick={() => setStep("confirm")}
              disabled={coins <= 0 || !quote || quote.net_cents <= 0}
            >
              Review
            </Button>
          </div>
        </div>
      )}

      {step === "confirm" && quote && (
        <div className="space-y-5">
          <p className="text-ink-dim">Confirm your withdrawal. This can&apos;t be undone once broadcast on-chain.</p>
          <div className="space-y-2.5 rounded-lg border border-border-soft bg-surface-high/30 p-4 font-mono text-[13px]">
            <QuoteRow k="Cashing out" v={`${fmt(coins)} credits`} />
            <QuoteRow k="You receive" v={`${usd(quote.net_cents)} USDC`} tone="text-primary" bold />
            <QuoteRow k="Destination" v="your linked Solana wallet" />
          </div>
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={() => setStep("amount")}>
              Back
            </Button>
            <Button onClick={submit} disabled={busy}>
              <ArrowUpRight className="h-4 w-4" /> {busy ? "Submitting…" : `Withdraw ${usd(quote.net_cents)}`}
            </Button>
          </div>
        </div>
      )}

      {step === "status" && wd && (
        <div className="space-y-5 py-1 text-center">
          {paid ? (
            <CheckCircle2 className="mx-auto h-12 w-12 text-primary" />
          ) : (
            <Loader2 className="mx-auto h-10 w-10 animate-spin text-ink-faint" />
          )}
          <div>
            <h3 className="font-display text-lg font-semibold text-ink-primary">
              {paid ? "Withdrawal sent" : wd.status === "rejected" || wd.status === "failed" ? "Withdrawal not completed" : "Withdrawal requested"}
            </h3>
            <p className="mt-2 text-ink-dim">
              {paid
                ? `${usd(wd.net_cents)} USDC was sent to your wallet.`
                : wd.status === "rejected"
                  ? "This request was rejected; your credits were returned."
                  : wd.status === "failed"
                    ? "The payout failed on-chain; your credits were returned."
                    : "Your request is queued for review and on-chain payout. Credits are held safely until it settles."}
            </p>
          </div>
          <div className="space-y-2 rounded-lg border border-border-soft bg-surface-high/30 p-4 text-left font-mono text-[12px]">
            <QuoteRow k="Status" v={wd.status} />
            <QuoteRow k="You receive" v={`${usd(wd.net_cents)} USDC`} />
            {wd.dest_wallet && <QuoteRow k="Destination" v={shorten(wd.dest_wallet)} />}
            {wd.transfer_id && <QuoteRow k="Tx" v={shorten(wd.transfer_id)} />}
          </div>
          <Button full onClick={onClose}>
            {terminal ? "Done" : "Close — we'll keep processing"}
          </Button>
        </div>
      )}

      {step === "error" && (
        <div className="space-y-5 py-2 text-center">
          <AlertCircle className="mx-auto h-12 w-12 text-status-error" />
          <p className="text-ink-dim">{error ?? "Something went wrong."}</p>
          <div className="flex justify-center gap-2">
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
            <Button onClick={() => setStep("amount")}>Try again</Button>
          </div>
        </div>
      )}
    </Modal>
  );
}

function QuoteRow({ k, v, tone, bold }: { k: string; v: string; tone?: string; bold?: boolean }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-ink-faint">{k}</span>
      <span className={cx(tone ?? "text-ink-dim", bold && "font-semibold")}>{v}</span>
    </div>
  );
}

function ErrorLine({ children }: { children: React.ReactNode }) {
  return (
    <p className="rounded-lg border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
      ✕ {children}
    </p>
  );
}

function shorten(s: string) {
  return s.length > 16 ? `${s.slice(0, 8)}…${s.slice(-6)}` : s;
}
