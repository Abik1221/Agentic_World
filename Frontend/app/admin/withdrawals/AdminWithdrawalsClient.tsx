"use client";

import { useCallback, useEffect, useState } from "react";
import { fmt } from "@/lib/mock";
import { approveWithdrawal, fetchAdminWithdrawals, rejectWithdrawal, type AdminWithdrawal } from "@/lib/api";
import { getSession } from "@/lib/session";
import { Badge, Button, Card, CardHeader, PageHeader } from "@/components/console/primitives";
import { SectionTabs } from "@/components/console/SectionTabs";

export function AdminWithdrawalsClient() {
  const [items, setItems] = useState<AdminWithdrawal[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const rows = await fetchAdminWithdrawals(getSession());
      setItems(rows);
      setErr(null);
    } catch (e) {
      setErr((e as Error)?.message ?? "Access denied or backend offline.");
      setItems([]);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function approve(id: string) {
    setBusy(id + ":approve");
    try {
      await approveWithdrawal(getSession(), id);
      await load();
    } catch (e) {
      setErr((e as Error)?.message ?? "Approve failed.");
    } finally {
      setBusy(null);
    }
  }

  async function reject(id: string) {
    setBusy(id + ":reject");
    try {
      await rejectWithdrawal(getSession(), id, "admin_review");
      await load();
    } catch (e) {
      setErr((e as Error)?.message ?? "Reject failed.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Withdrawal Queue"
        subtitle="Review pending cash-outs · approve only after the anti-fraud clearing window elapses"
      />
      <SectionTabs />

      {err && (
        <Card className="border-danger/40 p-4">
          <p className="font-mono text-sm text-danger">{err}</p>
          <p className="mt-2 font-mono text-[11px] text-fg-muted">
            Set your user id in backend <code className="text-fg">ADMIN_USER_IDS</code> and sign in with that dashboard token.
          </p>
        </Card>
      )}

      <Card className="p-5">
        <CardHeader title="Pending Requests" subtitle={`${items.length} awaiting review`} />
        <div className="mt-3 overflow-x-auto rounded-lg border border-line">
          <table className="w-full min-w-[760px] text-sm">
            <thead>
              <tr className="border-b border-line bg-panel-2/40 font-mono text-[10px] uppercase tracking-widest text-fg-muted">
                <th className="px-4 py-2.5 text-left font-medium">ID</th>
                <th className="px-4 py-2.5 text-left font-medium">Agent</th>
                <th className="px-4 py-2.5 text-left font-medium">Owner</th>
                <th className="px-4 py-2.5 text-right font-medium">Coins</th>
                <th className="px-4 py-2.5 text-right font-medium">Net</th>
                <th className="px-4 py-2.5 text-left font-medium">Requested</th>
                <th className="px-4 py-2.5 text-left font-medium">Clearing</th>
                <th className="px-4 py-2.5 text-right font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 ? (
                <tr>
                  <td colSpan={8} className="px-4 py-10 text-center font-mono text-xs text-fg-muted">
                    {err ? "—" : "No pending withdrawals."}
                  </td>
                </tr>
              ) : (
                items.map((w) => (
                  <tr key={w.withdrawal_id} className="border-b border-line/60 last:border-0">
                    <td className="px-4 py-2.5 font-mono text-[11px] text-fg-muted">{w.withdrawal_id.slice(0, 10)}…</td>
                    <td className="px-4 py-2.5 text-fg">{w.agent}</td>
                    <td className="px-4 py-2.5 text-fg-muted">{w.owner}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-fg">{fmt(w.coins)}</td>
                    <td className="px-4 py-2.5 text-right font-mono text-ok">${(w.net_cents / 100).toFixed(2)}</td>
                    <td className="px-4 py-2.5 font-mono text-[11px] text-fg-muted">{new Date(w.requested_at).toLocaleString()}</td>
                    <td className="px-4 py-2.5">
                      {w.can_approve ? <Badge tone="ok">Ready</Badge> : <span className="font-mono text-[11px] text-warn">{Math.ceil(w.clearing_wait_ms / 3600000)}h left</span>}
                    </td>
                    <td className="px-4 py-2.5">
                      <div className="flex justify-end gap-2">
                        <Button size="sm" disabled={!w.can_approve || busy !== null} onClick={() => approve(w.withdrawal_id)}>
                          Approve
                        </Button>
                        <Button size="sm" variant="ghost" disabled={busy !== null} onClick={() => reject(w.withdrawal_id)}>
                          Reject
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </Card>
    </div>
  );
}
