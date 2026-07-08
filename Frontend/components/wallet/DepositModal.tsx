"use client";

import * as React from "react";
import QRCode from "qrcode";
import { Check, Copy, Loader2, CheckCircle2, AlertCircle } from "lucide-react";
import { Modal } from "@/components/Modal";
import { Button, SectionLabel, cx } from "@/components/ui";
import { createDeposit, getDeposit, ApiError, type DepositSession } from "@/lib/api";
import { getSession } from "@/lib/session";

const PRESETS = [5, 10, 25, 50];
const USDC_DECIMALS = 1_000_000;
const POLL_MS = 4000;

type Step = "amount" | "pay" | "done" | "error";

// DepositModal walks the user through a Solana USDC deposit: pick an amount →
// scan/copy the Solana Pay request (QR) → the backend listener credits the
// treasury once the transfer finalizes, which we surface by polling the session.
export function DepositModal({
  open,
  onClose,
  onCredited,
}: {
  open: boolean;
  onClose: () => void;
  onCredited?: () => void;
}) {
  const [step, setStep] = React.useState<Step>("amount");
  const [amount, setAmount] = React.useState(10);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [session, setSession] = React.useState<DepositSession | null>(null);
  const [qr, setQr] = React.useState<string | null>(null);
  const [copied, setCopied] = React.useState(false);

  // Reset to a clean state each time the modal opens.
  React.useEffect(() => {
    if (open) {
      setStep("amount");
      setAmount(10);
      setError(null);
      setSession(null);
      setQr(null);
      setBusy(false);
    }
  }, [open]);

  async function start() {
    if (busy) return;
    if (!amount || amount <= 0) {
      setError("Enter an amount greater than zero.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const s = await createDeposit(getSession(), amount);
      setSession(s);
      QRCode.toDataURL(s.pay_url, { margin: 1, width: 224 }).then(setQr).catch(() => setQr(null));
      setStep("pay");
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.status === 404
            ? "Deposits aren't available yet. Please check back soon."
            : err.message
          : "Could not start the deposit. Please try again.",
      );
    } finally {
      setBusy(false);
    }
  }

  // Poll the session while awaiting payment; stop on a terminal status.
  React.useEffect(() => {
    if (step !== "pay" || !session) return;
    let alive = true;
    const id = setInterval(async () => {
      try {
        const s = await getDeposit(getSession(), session.deposit_id);
        if (!alive) return;
        setSession(s);
        if (s.status === "completed") {
          setStep("done");
          onCredited?.();
        } else if (s.status === "expired" || s.status === "failed") {
          setError(s.status === "expired" ? "This deposit request expired. Start a new one." : "The deposit failed on-chain.");
          setStep("error");
        }
      } catch {
        /* transient — keep polling */
      }
    }, POLL_MS);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [step, session, onCredited]);

  function copyAddress() {
    if (!session) return;
    navigator.clipboard?.writeText(session.recipient);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  const usdc = (base: number) => (base / USDC_DECIMALS).toLocaleString(undefined, { maximumFractionDigits: 6 });

  return (
    <Modal open={open} onClose={onClose} eyebrow="SOLANA · USDC" title="Deposit USDC" size="md">
      {step === "amount" && (
        <div className="space-y-5">
          <p className="text-ink-dim">
            Deposit USDC on Solana to get game credits instantly. You keep custody until you send —
            credits appear once the transfer confirms on-chain.
          </p>
          <div>
            <SectionLabel className="mb-2">Amount (USDC)</SectionLabel>
            <div className="mb-3 grid grid-cols-4 gap-2">
              {PRESETS.map((p) => (
                <button
                  key={p}
                  onClick={() => setAmount(p)}
                  className={cx(
                    "rounded-lg border px-3 py-2 font-mono text-sm transition",
                    amount === p
                      ? "border-primary bg-primary-container/15 text-primary"
                      : "border-border-soft text-ink-dim hover:bg-surface-high/60",
                  )}
                >
                  ${p}
                </button>
              ))}
            </div>
            <div className="flex items-center gap-2 rounded-lg border border-border-soft bg-surface-high/40 px-3 py-2">
              <span className="font-mono text-ink-faint">$</span>
              <input
                type="number"
                min={1}
                step="0.000001"
                value={amount}
                onChange={(e) => setAmount(Number(e.target.value))}
                className="w-full bg-transparent font-mono text-sm text-ink-primary outline-none"
              />
              <span className="font-mono text-[11px] text-ink-faint">≈ {(amount * 100).toLocaleString()} credits</span>
            </div>
          </div>
          {error && <ErrorLine>{error}</ErrorLine>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button onClick={start} disabled={busy}>
              {busy ? "Creating…" : "Continue"}
            </Button>
          </div>
        </div>
      )}

      {step === "pay" && session && (
        <div className="space-y-5">
          <div className="flex flex-col items-center gap-3">
            <div className="rounded-xl bg-white p-3">
              {qr ? (
                // eslint-disable-next-line @next/next/no-img-element
                <img src={qr} alt="Solana Pay QR" width={200} height={200} className="h-[200px] w-[200px]" />
              ) : (
                <div className="flex h-[200px] w-[200px] items-center justify-center">
                  <Loader2 className="h-6 w-6 animate-spin text-ink-faint" />
                </div>
              )}
            </div>
            <p className="text-center font-display text-lg font-semibold text-ink-primary">
              {usdc(session.amount_base)} USDC
              <span className="ml-2 font-mono text-sm text-ink-faint">→ {session.coins_expected.toLocaleString()} credits</span>
            </p>
          </div>

          <div>
            <SectionLabel className="mb-1.5">Recipient</SectionLabel>
            <div className="flex items-center gap-2 rounded-lg border border-border-soft bg-surface-high/40 px-3 py-2">
              <code className="flex-1 truncate font-mono text-[12px] text-ink-dim">{session.recipient}</code>
              <button onClick={copyAddress} className="shrink-0 text-primary hover:text-tertiary" aria-label="Copy address">
                {copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
              </button>
            </div>
            <p className="mt-2 font-mono text-[11px] leading-5 text-ink-faint">
              Scan with Phantom / Solflare / Backpack, or send exactly {usdc(session.amount_base)} USDC to the
              address above. Sending a different token or amount won&apos;t be credited.
            </p>
          </div>

          <div className="flex items-center justify-center gap-2 rounded-lg border border-border-soft bg-surface-high/30 px-3 py-2.5">
            <span className={cx("live-dot inline-block h-2 w-2 rounded-full", session.status === "detected" ? "bg-secondary" : "bg-primary")} />
            <span className="font-mono text-[12px] text-ink-dim">
              {session.status === "detected" ? "Payment detected — confirming on-chain…" : "Waiting for your payment…"}
            </span>
          </div>

          <div className="flex justify-end">
            <Button variant="ghost" onClick={onClose}>
              Close
            </Button>
          </div>
          <p className="text-center font-mono text-[10px] text-ink-faint">
            You can close this — credits are added automatically once the transfer confirms.
          </p>
        </div>
      )}

      {step === "done" && session && (
        <div className="space-y-5 py-2 text-center">
          <CheckCircle2 className="mx-auto h-12 w-12 text-primary" />
          <div>
            <h3 className="font-display text-xl font-semibold text-ink-primary">Deposit complete</h3>
            <p className="mt-2 text-ink-dim">
              {(session.coins_credited ?? session.coins_expected).toLocaleString()} credits were added to your wallet.
            </p>
          </div>
          <Button full onClick={onClose}>
            Done
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

function ErrorLine({ children }: { children: React.ReactNode }) {
  return (
    <p className="rounded-lg border border-status-error/40 bg-status-error/10 px-3 py-2 font-mono text-[11px] text-status-error">
      ✕ {children}
    </p>
  );
}
