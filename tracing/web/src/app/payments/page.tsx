import { Unavailable } from "@/components/Unavailable";
import { fetchArena } from "@/lib/pyyol-lens-api";

export const dynamic = "force-dynamic";

type Failure = {
  user: string;
  flow: string;
  ref: string;
  stage: string;
  detail?: string;
  at: string;
};

type MoneySummary = {
  failed_attempts: number;
  unique_users_failed: number;
  deposit_failures: number;
  withdrawal_failures: number;
  topup_failures: number;
  deposits_completed: number;
  withdrawals_paid: number;
  withdrawals_rejected: number;
  withdrawals_pending: number;
  open_payout_holds: number;
};

type FailuresResponse = {
  failures?: Failure[];
  summary?: MoneySummary;
};

/**
 * Deposits and withdrawals that broke mid-flow — the arena payment log, not Lens
 * ClickHouse. Model-cost pages cannot answer "who tried to cash out and failed".
 */
export default async function PaymentsPage() {
  const res = await fetchArena<FailuresResponse>("/v1/admin/payments/failures?limit=100");

  if (!res.ok) {
    return (
      <main className="page">
        <section className="hero">
          <div>
            <h1>Payments</h1>
            <p>Failed deposits, withdrawals, and the people who tried.</p>
          </div>
        </section>
        <Unavailable
          title="Payment trace unavailable"
          detail={
            res.reason === "unconfigured"
              ? "PYYOL_API_URL is not set, so this deployment cannot reach the arena payment log."
              : res.reason === "unauthorized"
                ? "Your session is not authorised for the arena admin API."
                : "The arena API did not answer. This is a connectivity problem, not an empty log."
          }
        />
      </main>
    );
  }

  const failures = res.data.failures ?? [];
  const s = res.data.summary;

  return (
    <main className="page">
      <section className="hero">
        <div>
          <h1>Payments</h1>
          <p>
            Completed vs failed vs held. Failed attempts come from the payment log;
            paid withdrawals and open payout holds come from the ledger.
          </p>
        </div>
      </section>

      {s ? (
        <section className="cards">
          <div className="card">
            <div className="card-label">Failed attempts</div>
            <div className="card-value">{s.failed_attempts.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">People who failed</div>
            <div className="card-value">{s.unique_users_failed.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">Deposits completed</div>
            <div className="card-value">{s.deposits_completed.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">Withdrawals paid</div>
            <div className="card-value">{s.withdrawals_paid.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">Rejected / failed payouts</div>
            <div className="card-value">{s.withdrawals_rejected.toLocaleString()}</div>
          </div>
          <div className="card">
            <div className="card-label">Payout holds</div>
            <div className="card-value">{s.open_payout_holds.toLocaleString()}</div>
          </div>
        </section>
      ) : null}

      <section className="panel">
        <div className="console-header">
          <h2>Broken payments</h2>
          <p className="table-count">
            {failures.length.toLocaleString()} mid-flow
            {s ? ` · ${s.deposit_failures} deposit · ${s.withdrawal_failures} withdrawal · ${s.topup_failures} topup` : ""}
          </p>
        </div>
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>User</th>
                <th>Flow</th>
                <th>Ref</th>
                <th>Died at</th>
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {failures.length ? (
                failures.map((f) => (
                  <tr key={`${f.flow}-${f.ref}-${f.stage}-${f.at}`}>
                    <td className="muted">{f.at ? new Date(f.at).toISOString().replace("T", " ").slice(0, 19) : "—"}</td>
                    <td className="mono">{f.user}</td>
                    <td>{f.flow}</td>
                    <td className="mono">{f.ref}</td>
                    <td className="status-error">{f.stage.replace(/_/g, " ")}</td>
                    <td>{f.detail || "—"}</td>
                  </tr>
                ))
              ) : (
                <tr>
                  <td colSpan={6} className="empty-state">
                    No mid-flow failures recorded. This is an all-clear, not missing data.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </main>
  );
}
