"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { fmt } from "@/lib/mock";
import {
  allocateToAgent,
  fetchCoinPacks,
  fetchUserWallet,
  fetchUserWalletHistory,
  fetchWithdrawals,
  type UserWalletSummary,
  type WalletTxn,
  type Withdrawal,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import type { CoinPack } from "@/lib/mock";
import { CoinPackCheckout } from "@/components/payments/CoinPackCheckout";
import { StripeBadge } from "@/components/payments/StripeBadge";

export function WalletDashboard() {
  const [summary, setSummary] = useState<UserWalletSummary | null>(null);
  const [txns, setTxns] = useState<WalletTxn[]>([]);
  const [withdrawals, setWithdrawals] = useState<Withdrawal[]>([]);
  const [packs, setPacks] = useState<CoinPack[]>([]);
  const [loading, setLoading] = useState(true);

  async function reload() {
    const session = getSession();
    const [s, h, w, p] = await Promise.all([
      fetchUserWallet(session),
      session.dashboardToken ? fetchUserWalletHistory(session) : Promise.resolve([]),
      session.dashboardToken ? fetchWithdrawals(session) : Promise.resolve([]),
      fetchCoinPacks(session),
    ]);
    setSummary(s);
    setTxns(h);
    setWithdrawals(w);
    setPacks(p);
    setLoading(false);
  }

  useEffect(() => {
    reload();
  }, []);

  const coinCents = summary?.coin_cents ?? 1;

  return (
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div>
            <SectionLabel className="mb-3 text-primary">FINANCIAL_HUB</SectionLabel>
            <h1 className="font-display text-4xl font-bold tracking-[-0.5px]">Wallet</h1>
            <p className="mt-2 max-w-2xl text-ink-dim">
              Deposits via Stripe land in your treasury. Allocate to agents, compete, and cash out net
              winnings through Stripe Connect.
            </p>
          </div>
          <StripeBadge />
        </div>

        {loading ? (
          <p className="mt-10 font-mono text-sm text-ink-faint">Loading wallet…</p>
        ) : summary ? (
          <div className="mt-8 grid gap-6 lg:grid-cols-[1.4fr_1fr]">
            <div className="space-y-6">
              <Panel glass className="p-7">
                <SectionLabel className="text-secondary">TREASURY BALANCE</SectionLabel>
                <div className="mt-3 font-mono text-5xl font-semibold tabular-nums text-primary">
                  {fmt(summary.available_balance)}{" "}
                  <span className="text-xl text-ink-faint">CRD</span>
                </div>
                <div className="mt-1 font-mono text-sm text-ink-dim">
                  ≈ ${((summary.available_balance * coinCents) / 100).toFixed(2)} USD
                </div>
                <div className="mt-6 grid grid-cols-2 gap-4 border-t border-border-soft pt-5 sm:grid-cols-4">
                  <Stat label="LOCKED" value={fmt(summary.locked_balance)} />
                  <Stat label="PENDING" value={fmt(summary.pending_balance)} />
                  <Stat label="DEPOSITS" value={fmt(summary.lifetime_deposits)} />
                  <Stat label="WINNINGS" value={fmt(summary.tournament_winnings)} tone="text-primary" />
                </div>
              </Panel>

              <AgentAllocation summary={summary} onAllocated={reload} />
              <CoinPackCheckout packs={packs} />
              <TransactionHistory txns={txns} />
            </div>

            <div className="space-y-6">
              <Panel className="p-6">
                <SectionLabel className="mb-4">AGENT WALLETS</SectionLabel>
                {summary.agents.length === 0 ? (
                  <p className="font-mono text-[12px] text-ink-faint">Register an agent to allocate coins.</p>
                ) : (
                  <div className="space-y-3">
                    {summary.agents.map((a) => (
                      <div key={a.agent} className="rounded-lg border border-border-strong bg-bg-deep/50 p-4">
                        <div className="flex items-center justify-between">
                          <span className="font-mono text-sm text-ink-primary">{a.name || a.agent}</span>
                          {a.active_matches > 0 && (
                            <Pill tone="amber" className="text-[10px]">
                              IN MATCH
                            </Pill>
                          )}
                        </div>
                        <div className="mt-2 font-mono text-2xl font-semibold tabular-nums">
                          {fmt(a.balance)} <span className="text-xs text-ink-faint">CRD</span>
                        </div>
                        <div className="mt-2 flex justify-between font-mono text-[11px] text-ink-faint">
                          <span>Locked: {fmt(a.locked_in_matches)}</span>
                          <span className="text-primary">Withdrawable: {fmt(a.withdrawable)}</span>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
                <div className="mt-5 grid grid-cols-2 gap-2">
                  <Link href="/guardrails" className="btn-primary text-center text-sm">
                    Cash out
                  </Link>
                  <Link href="/subscription" className="btn-ghost text-center text-sm">
                    Arena Pass
                  </Link>
                </div>
              </Panel>

              <Panel className="p-6">
                <SectionLabel className="mb-4">RECENT WITHDRAWALS</SectionLabel>
                {withdrawals.length === 0 ? (
                  <p className="font-mono text-[12px] text-ink-faint">No withdrawal requests yet.</p>
                ) : (
                  <div className="space-y-2">
                    {withdrawals.slice(0, 5).map((w) => (
                      <div key={w.withdrawal_id} className="flex justify-between font-mono text-[12px]">
                        <span className="text-ink-dim">{w.withdrawal_id.slice(0, 12)}…</span>
                        <span className={w.status === "paid" ? "text-primary" : "text-ink-primary"}>
                          {w.status} · ${(w.net_cents / 100).toFixed(2)}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
                <Link href="/withdrawals" className="btn-ghost mt-4 block w-full text-center text-sm">
                  Track withdrawals
                </Link>
              </Panel>
            </div>
          </div>
        ) : null}
      </div>
      <Footer />
    </div>
  );
}

function Stat({ label, value, tone }: { label: string; value: string; tone?: string }) {
  return (
    <div>
      <div className={cx("font-mono text-lg font-semibold tabular-nums", tone ?? "text-ink-primary")}>{value}</div>
      <div className="mt-1 label-caps">{label}</div>
    </div>
  );
}

function AgentAllocation({
  summary,
  onAllocated,
}: {
  summary: UserWalletSummary;
  onAllocated: () => void;
}) {
  const [agent, setAgent] = useState(summary.agents[0]?.agent ?? "");
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  async function go() {
    const session = getSession();
    const n = parseInt(amount, 10);
    if (!agent || !n || n <= 0) return setMsg("Pick an agent and a positive amount.");
    setBusy(true);
    setMsg(null);
    try {
      await allocateToAgent(session, agent, n);
      setMsg(`✓ Allocated ${fmt(n)} CRD`);
      setAmount("");
      onAllocated();
    } catch (e) {
      setMsg("✕ " + ((e as Error)?.message ?? "Allocation failed."));
    } finally {
      setBusy(false);
    }
  }

  if (summary.agents.length === 0) return null;

  return (
    <Panel className="p-6">
      <SectionLabel className="mb-4">FUND AGENTS</SectionLabel>
      <p className="mb-4 font-mono text-[12px] text-ink-faint">
        Move coins from treasury to an agent. Rebalancing is blocked during active matches.
      </p>
      <div className="flex flex-col gap-3 sm:flex-row">
        <select className="input flex-1" value={agent} onChange={(e) => setAgent(e.target.value)}>
          {summary.agents.map((a) => (
            <option key={a.agent} value={a.agent}>
              {a.name || a.agent} ({fmt(a.balance)} CRD)
            </option>
          ))}
        </select>
        <input
          className="input w-full sm:w-36"
          placeholder="Amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
        <button type="button" onClick={go} disabled={busy} className="btn-primary disabled:opacity-50">
          {busy ? "…" : "Allocate"}
        </button>
      </div>
      {msg && (
        <p className={cx("mt-2 font-mono text-[11px]", msg.startsWith("✓") ? "text-primary" : "text-status-error")}>
          {msg}
        </p>
      )}
    </Panel>
  );
}

function TransactionHistory({ txns }: { txns: WalletTxn[] }) {
  return (
    <Panel className="p-6">
      <SectionLabel className="mb-4">TRANSACTION HISTORY</SectionLabel>
      {txns.length === 0 ? (
        <p className="font-mono text-[12px] text-ink-faint">No treasury transactions yet.</p>
      ) : (
        <div className="max-h-64 overflow-y-auto">
          <table className="w-full font-mono text-[12px]">
            <thead>
              <tr className="text-left text-ink-faint">
                <th className="pb-2">Type</th>
                <th className="pb-2">Amount</th>
                <th className="pb-2">When</th>
              </tr>
            </thead>
            <tbody>
              {txns.map((t) => (
                <tr key={t.txn_id} className="border-t border-border-soft/50">
                  <td className="py-2 text-ink-dim">{t.kind}</td>
                  <td className={cx("py-2 tabular-nums", t.amount >= 0 ? "text-primary" : "text-status-error")}>
                    {t.amount >= 0 ? "+" : ""}
                    {fmt(t.amount)}
                  </td>
                  <td className="py-2 text-ink-faint">{new Date(t.created_at).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
