package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Runs the real audit against a real database.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run LedgerAudit
//
// Skipped without that variable, because an audit test that silently passes with no database is
// worse than no test: it reports green about money it never looked at.
func TestLedgerAuditAgainstLiveDatabase(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to audit a real ledger")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer pool.Close()

	rep, err := (&LedgerRepo{db: pool}).AuditLedger(ctx)
	if err != nil {
		t.Fatalf("audit failed to run: %v", err)
	}
	t.Logf("audited %d transactions / %d entries / %d wallets",
		rep.Transactions, rep.Entries, rep.Wallets)

	// An audit over an EMPTY ledger proves nothing and would pass trivially. Say so rather than
	// letting a green tick stand in for a check that had no rows to examine.
	if rep.Entries == 0 {
		t.Skip("ledger is empty — nothing to audit")
	}
	for _, f := range rep.Findings {
		t.Errorf("[%s] %s: %d row(s) — %s", f.Severity, f.Check, f.Count, f.Detail)
	}
	if !rep.Healthy {
		t.Fatal("ledger integrity violated; coins are not conserved")
	}
}
