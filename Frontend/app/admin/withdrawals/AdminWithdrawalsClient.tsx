"use client";

import { useCallback, useEffect, useState } from "react";
import { TopNav } from "@/components/Nav";
import { Footer } from "@/components/Footer";
import { Panel, Pill, SectionLabel, cx } from "@/components/ui";
import { fmt } from "@/lib/mock";
import {
  approveWithdrawal,
  fetchAdminWithdrawals,
  rejectWithdrawal,
  type AdminWithdrawal,
} from "@/lib/api";
import { getSession } from "@/lib/session";

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
    <div className="min-h-screen">
      <TopNav />
      <div className="mx-auto max-w-container px-6 py-10">
        <SectionLabel className="mb-3 text-secondary">ADMIN</SectionLabel>
        <h1 className="font-display text-3xl font-bold">Withdrawal approval queue</h1>
        <p className="mt-2 max-w-xl text-ink-dim">
          Review pending cash-out requests. Approve only after the anti-fraud clearing window elapses
          and the agent is not flagged.
        </p>

        {err && (
          <Panel className="mt-6 border-status-error/40 p-4">
            <p className="font-mono text-sm text-status-error">{err}</p>
            <p className="mt-2 font-mono text-[11px] text-ink-faint">
              Set your user id in backend <code>ADMIN_USER_IDS</code> and sign in with that dashboard token.
            </p>
          </Panel>
        )}

        <Panel className="mt-8 overflow-x-auto p-0">
          <table className="w-full min-w-[720px] font-mono text-[12px]">
            <thead>
              <tr className="border-b border-border-soft text-left text-ink-faint">
                <th className="p-4">ID</th>
                <th className="p-4">Agent</th>
                <th className="p-4">Owner</th>
                <th className="p-4">Coins</th>
                <th className="p-4">Net</th>
                <th className="p-4">Requested</th>
                <th className="p-4">Clearing</th>
                <th className="p-4">Actions</th>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 ? (
                <tr>
                  <td colSpan={8} className="p-8 text-center text-ink-faint">
                    {err ? "—" : "No pending withdrawals."}
                  </td>
                </tr>
              ) : (
                items.map((w) => (
                  <tr key={w.withdrawal_id} className="border-b border-border-soft/50">
                    <td className="p-4 text-ink-dim">{w.withdrawal_id.slice(0, 10)}…</td>
                    <td className="p-4">{w.agent}</td>
                    <td className="p-4">{w.owner}</td>
                    <td className="p-4">{fmt(w.coins)}</td>
                    <td className="p-4 text-primary">${(w.net_cents / 100).toFixed(2)}</td>
                    <td className="p-4 text-ink-faint">{new Date(w.requested_at).toLocaleString()}</td>
                    <td className="p-4">
                      {w.can_approve ? (
                        <Pill tone="teal">Ready</Pill>
                      ) : (
                        <span className="text-amber-400">
                          {Math.ceil(w.clearing_wait_ms / 3600000)}h left
                        </span>
                      )}
                    </td>
                    <td className="p-4">
                      <div className="flex gap-2">
                        <button
                          disabled={!w.can_approve || busy !== null}
                          onClick={() => approve(w.withdrawal_id)}
                          className="btn-primary px-3 py-1 text-[11px] disabled:opacity-40"
                        >
                          Approve
                        </button>
                        <button
                          disabled={busy !== null}
                          onClick={() => reject(w.withdrawal_id)}
                          className="btn-ghost px-3 py-1 text-[11px] disabled:opacity-40"
                        >
                          Reject
                        </button>
                      </div>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </Panel>
      </div>
      <Footer />
    </div>
  );
}
