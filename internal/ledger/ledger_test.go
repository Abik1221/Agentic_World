package ledger_test

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/prometheus/client_golang/prometheus"
)

// fakeRepo captures Apply inputs and returns a configurable result/error.
type fakeRepo struct {
	applied []ledger.ApplyInput
	result  ledger.ApplyResult
	err     error
}

func (f *fakeRepo) Apply(_ context.Context, in ledger.ApplyInput) (ledger.ApplyResult, error) {
	f.applied = append(f.applied, in)
	if f.err != nil {
		return ledger.ApplyResult{}, f.err
	}
	if f.result.PublicID == "" {
		return ledger.ApplyResult{PublicID: in.PublicID, Applied: true}, nil
	}
	return f.result, nil
}
func (f *fakeRepo) Balance(context.Context, string) (int64, error)            { return 0, nil }
func (f *fakeRepo) History(context.Context, string, int) ([]ledger.Line, error) { return nil, nil }
func (f *fakeRepo) Reconcile(context.Context) ([]ledger.Drift, error)         { return nil, nil }

func newSvc(repo ledger.Repo) *ledger.Service {
	return ledger.New(repo, prometheus.NewRegistry())
}

func TestPostRejectsEmpty(t *testing.T) {
	repo := &fakeRepo{}
	if _, err := newSvc(repo).Post(context.Background(), ledger.Txn{Kind: ledger.KindStake, Key: "k"}); err != ledger.ErrEmpty {
		t.Fatalf("empty postings = %v, want ErrEmpty", err)
	}
	if len(repo.applied) != 0 {
		t.Fatal("repo should not be touched for an empty txn")
	}
}

func TestPostRejectsUnbalanced(t *testing.T) {
	repo := &fakeRepo{}
	txn := ledger.Txn{Kind: ledger.KindStake, Key: "k", Postings: []ledger.Posting{
		{Wallet: ledger.AgentWallet("ag_a"), Amount: -50},
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: 40}, // off by 10
	}}
	if _, err := newSvc(repo).Post(context.Background(), txn); err != ledger.ErrUnbalanced {
		t.Fatalf("unbalanced = %v, want ErrUnbalanced", err)
	}
	if len(repo.applied) != 0 {
		t.Fatal("repo should not be touched for an unbalanced txn")
	}
}

func TestPostAppliesBalanced(t *testing.T) {
	repo := &fakeRepo{}
	txn := ledger.Txn{Kind: ledger.KindStake, Key: "stake:m_1:ag_a", Postings: []ledger.Posting{
		{Wallet: ledger.AgentWallet("ag_a"), Amount: -50},
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: 50},
	}}
	res, err := newSvc(repo).Post(context.Background(), txn)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !res.Applied {
		t.Fatal("want Applied=true")
	}
	if len(repo.applied) != 1 {
		t.Fatalf("repo.Apply called %d times, want 1", len(repo.applied))
	}
	got := repo.applied[0]
	if got.Key != "stake:m_1:ag_a" || got.Kind != ledger.KindStake {
		t.Fatalf("forwarded kind/key wrong: %+v", got)
	}
	if got.PublicID == "" {
		t.Fatal("service must mint a txn public id")
	}
}

func TestPostIdempotentReplay(t *testing.T) {
	repo := &fakeRepo{result: ledger.ApplyResult{PublicID: "txn_existing", Applied: false}}
	txn := ledger.Txn{Kind: ledger.KindSettle, Key: "settle:m_1", Postings: []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -100},
		{Wallet: ledger.AgentWallet("ag_w"), Amount: 100},
	}}
	res, err := newSvc(repo).Post(context.Background(), txn)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if res.Applied {
		t.Fatal("replay must report Applied=false")
	}
	if res.PublicID != "txn_existing" {
		t.Fatalf("replay returned %q, want the pre-existing txn", res.PublicID)
	}
}
