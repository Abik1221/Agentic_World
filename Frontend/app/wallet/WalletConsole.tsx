"use client";

import * as React from "react";
import Link from "next/link";
import { ArrowDownRight, ArrowUpRight, Bot, CircleDollarSign, Coins, Landmark, Lock, TrendingUp, Wallet as WalletIcon } from "lucide-react";
import {
  fetchCoinPacks,
  fetchUserWallet,
  fetchUserWalletHistory,
  fetchWithdrawals,
  type UserWalletSummary,
  type WalletTxn,
  type Withdrawal,
} from "@/lib/api";
import { getSession } from "@/lib/session";
import { fmt, type CoinPack } from "@/lib/mock";
import { CoinPackCheckout } from "@/components/payments/CoinPackCheckout";
import {
  Button,
  Card,
  CardHeader,
  DataTable,
  KpiCard,
  PageHeader,
  StatusBadge,
  SubNav,
  type Column,
} from "@/components/console/primitives";
import { AreaTrend, Donut, DonutLegend, CHART } from "@/components/console/charts";

type Tab = "overview" | "transactions" | "withdrawals" | "buy";

export function WalletConsole() {
  const [summary, setSummary] = React.useState<UserWalletSummary | null>(null);
  const [txns, setTxns] = React.useState<WalletTxn[]>([]);
  const [withdrawals, setWithdrawals] = React.useState<Withdrawal[]>([]);
  const [packs, setPacks] = React.useState<CoinPack[]>([]);
  const [tab, setTab] = React.useState<Tab>("overview");
  const [loading, setLoading] = React.useState(true);

  React.useEffect(() => {
    (async () => {
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
    })();
  }, []);

  const cc = summary?.coin_cents ?? 1;
  const usd = (coins: number) => `$${((coins * cc) / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
  const netWorth = summary ? summary.available_balance + summary.locked_balance + summary.pending_balance : 0;

  const composition = summary
    ? [
        { name: "Available", value: summary.available_balance, color: CHART.ok },
        { name: "Locked", value: summary.locked_balance, color: CHART.brand },
        { name: "Pending", value: summary.pending_balance, color: CHART.warn },
      ].filter((d) => d.value > 0)
    : [];

  // Running-balance trend derived from real transactions (oldest → newest).
  const trend = React.useMemo(() => {
    const ordered = [...txns].reverse();
    let running = 0;
    const pts = ordered.map((t, i) => {
      running += t.amount;
      return { label: `#${i + 1}`, value: running };
    });
    return pts.slice(-16);
  }, [txns]);

  const txnColumns: Column<WalletTxn>[] = [
    { key: "kind", header: "Type", render: (t) => <span className="capitalize">{t.kind.replace(/_/g, " ")}</span> },
    {
      key: "amount",
      header: "Amount",
      render: (t) => (
        <span className={t.amount >= 0 ? "font-mono text-ok" : "font-mono text-danger"}>
          {t.amount >= 0 ? "+" : ""}
          {fmt(t.amount)}
        </span>
      ),
    },
    { key: "usd", header: "USD", align: "right", render: (t) => <span className="font-mono text-fg-muted">{usd(Math.abs(t.amount))}</span> },
    { key: "date", header: "Date", align: "right", render: (t) => <span className="font-mono text-[11px] text-fg-muted">{new Date(t.created_at).toLocaleString()}</span> },
  ];

  const wdColumns: Column<Withdrawal>[] = [
    { key: "id", header: "ID", render: (w) => <span className="font-mono text-[11px] text-fg-muted">{w.withdrawal_id.slice(0, 10)}</span> },
    { key: "coins", header: "Coins", render: (w) => <span className="font-mono">{fmt(w.coins)}</span> },
    { key: "fee", header: "Fee", render: (w) => <span className="font-mono text-fg-muted">{fmt(w.fee_coins)}</span> },
    { key: "net", header: "Net", render: (w) => <span className="font-mono text-fg">${(w.net_cents / 100).toFixed(2)}</span> },
    { key: "status", header: "Status", render: (w) => <StatusBadge status={w.status} /> },
    { key: "date", header: "Requested", align: "right", render: (w) => <span className="font-mono text-[11px] text-fg-muted">{new Date(w.requested_at).toLocaleDateString()}</span> },
  ];

  const tabs: { key: Tab; label: string; count?: number }[] = [
    { key: "overview", label: "Overview" },
    { key: "transactions", label: "Transactions", count: txns.length },
    { key: "withdrawals", label: "Withdrawals", count: withdrawals.length },
    { key: "buy", label: "Buy Coins" },
  ];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Wallet"
        subtitle="Treasury · agent balances · transactions"
        actions={
          <>
            <Button asChild variant="outline" size="sm">
              <Link href="/withdrawals">Cash out</Link>
            </Button>
            <Button size="sm" onClick={() => setTab("buy")}>
              Buy coins
            </Button>
          </>
        }
      />

      <SubNav items={tabs} active={tab} onSelect={setTab} />

      {loading ? (
        <div className="flex h-40 items-center justify-center font-mono text-xs text-fg-muted">Loading wallet…</div>
      ) : !summary ? (
        <div className="flex h-40 items-center justify-center font-mono text-xs text-fg-muted">Wallet unavailable</div>
      ) : tab === "overview" ? (
        <div className="space-y-5">
          <div className="grid grid-cols-2 gap-3 xl:grid-cols-3 2xl:grid-cols-6">
            <KpiCard label="Available" value={fmt(summary.available_balance)} sub={usd(summary.available_balance)} icon={WalletIcon} />
            <KpiCard label="Locked" value={fmt(summary.locked_balance)} sub="in matches" icon={Lock} />
            <KpiCard label="Pending" value={fmt(summary.pending_balance)} sub="clearing" icon={CircleDollarSign} />
            <KpiCard label="Net Worth" value={fmt(netWorth)} sub={usd(netWorth)} icon={TrendingUp} trend="total" trendUp />
            <KpiCard label="Lifetime Earnings" value={fmt(summary.lifetime_earnings)} icon={Coins} />
            <KpiCard label="Deposits" value={fmt(summary.lifetime_deposits)} sub={usd(summary.lifetime_deposits)} icon={Landmark} />
          </div>

          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <Card className="p-5 lg:col-span-2">
              <CardHeader title="Balance Trend" subtitle="Running balance across recent transactions" />
              <div className="mt-4">
                {trend.length > 1 ? (
                  <AreaTrend data={trend} dataKey="value" xKey="label" color={CHART.brand} id="walletGrad" />
                ) : (
                  <div className="flex h-40 items-center justify-center font-mono text-xs text-fg-muted">No transaction history yet</div>
                )}
              </div>
            </Card>
            <Card className="p-5">
              <CardHeader title="Balance Composition" subtitle={fmt(netWorth) + " total"} />
              <div className="mt-4 flex items-center gap-4">
                {composition.length ? (
                  <>
                    <Donut data={composition} />
                    <div className="flex-1">
                      <DonutLegend data={composition} />
                    </div>
                  </>
                ) : (
                  <p className="font-mono text-xs text-fg-muted">Empty wallet</p>
                )}
              </div>
            </Card>
          </div>

          <Card className="p-5">
            <CardHeader title="Agent Balances" subtitle={`${summary.agents.length} agents`} />
            <div className="mt-3">
              <DataTable
                rows={summary.agents}
                empty="No agents funded yet"
                columns={[
                  { key: "name", header: "Agent", render: (a) => (
                    <span className="flex items-center gap-2">
                      <Bot className="h-3.5 w-3.5 text-brand" />
                      <span className="text-fg">{a.name || a.agent.slice(0, 10)}</span>
                    </span>
                  ) },
                  { key: "balance", header: "Balance", render: (a) => <span className="font-mono">{fmt(a.balance)}</span> },
                  { key: "locked", header: "Locked", render: (a) => <span className="font-mono text-fg-muted">{fmt(a.locked_in_matches)}</span> },
                  { key: "withdrawable", header: "Withdrawable", render: (a) => <span className="font-mono text-ok">{fmt(a.withdrawable)}</span> },
                  { key: "active", header: "Matches", align: "right", render: (a) => <span className="font-mono text-fg-muted">{a.active_matches}</span> },
                ]}
              />
            </div>
          </Card>
        </div>
      ) : tab === "transactions" ? (
        <Card className="p-5">
          <CardHeader title="Transactions" subtitle={`${txns.length} records`} />
          <div className="mt-3">
            <DataTable rows={txns} columns={txnColumns} empty="No transactions yet — buy coins or play a match" />
          </div>
        </Card>
      ) : tab === "withdrawals" ? (
        <Card className="p-5">
          <CardHeader title="Withdrawals" subtitle={`${withdrawals.length} requests`} action={
            <Button asChild variant="outline" size="sm"><Link href="/withdrawals">New withdrawal</Link></Button>
          } />
          <div className="mt-3">
            <DataTable rows={withdrawals} columns={wdColumns} empty="No withdrawals requested" />
          </div>
        </Card>
      ) : (
        <Card className="p-5">
          <CardHeader title="Buy Coins" subtitle="Top up your treasury via Stripe" />
          <div className="mt-4">
            <CoinPackCheckout packs={packs} />
          </div>
        </Card>
      )}
    </div>
  );
}
