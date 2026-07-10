//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/identity"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestUpsertUserFromPrivy_NoEmailTakeover is the regression guard for the W1
// account-takeover fix. A Privy login is keyed STRICTLY on the cryptographically
// proven privy_user_id (a DID). A self-asserted profile email — the only thing an
// attacker controls — must NEVER (a) link the login to a pre-existing account with
// that email (account takeover), nor (b) be written into the unique `email` column
// (email squatting). See internal/store/identity_repo.go:UpsertUserFromPrivy.
func TestUpsertUserFromPrivy_NoEmailTakeover(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	repo := NewIdentityRepo(pool)

	const (
		victimPublic  = "usr_w1_victim"
		victimEmail   = "w1-victim@example.test"
		attackerPrivy = "did:privy:w1-attacker"
	)

	cleanup := func() {
		_, _ = pool.Exec(ctx,
			`DELETE FROM wallets WHERE user_id IN (SELECT id FROM users WHERE public_id=$1 OR privy_user_id=$2)`,
			victimPublic, attackerPrivy)
		_, _ = pool.Exec(ctx,
			`DELETE FROM users WHERE public_id=$1 OR privy_user_id=$2`,
			victimPublic, attackerPrivy)
	}
	cleanup()
	defer cleanup()

	// A pre-existing email/password account (privy_user_id IS NULL) — the target.
	var victimID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id, email) VALUES ($1,$2) RETURNING id`,
		victimPublic, victimEmail).Scan(&victimID); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	// Attacker authenticates with THEIR OWN Privy id but passes the victim's email
	// as a profile hint — the exact W1 attack.
	gotPublic, created, err := repo.UpsertUserFromPrivy(ctx, identity.PrivyUpsertInput{
		PrivyUserID:  attackerPrivy,
		UserPublicID: "usr_w1_attacker",
		Profile:      identity.PrivyProfile{Email: victimEmail, DisplayName: "attacker"},
	})
	if err != nil {
		t.Fatalf("privy upsert: %v", err)
	}

	// It must be a brand-new, distinct account — never the victim's.
	if !created {
		t.Fatal("W1 regression: privy login linked to an existing account (created=false) — takeover")
	}
	if gotPublic == victimPublic {
		t.Fatalf("W1 regression: privy login resolved to the victim's account %q", victimPublic)
	}

	// Victim untouched: still owns the email, still has NO privy id attached.
	var victimPrivy *string
	if err := pool.QueryRow(ctx,
		`SELECT privy_user_id FROM users WHERE public_id=$1`, victimPublic).
		Scan(&victimPrivy); err != nil {
		t.Fatalf("reload victim: %v", err)
	}
	if victimPrivy != nil {
		t.Fatalf("W1 regression: victim account had a privy_user_id attached: %v", *victimPrivy)
	}

	// The attacker's new row must NOT have squatted the victim's email.
	var attackerEmail *string
	if err := pool.QueryRow(ctx,
		`SELECT email FROM users WHERE privy_user_id=$1`, attackerPrivy).
		Scan(&attackerEmail); err != nil {
		t.Fatalf("reload attacker: %v", err)
	}
	if attackerEmail != nil {
		t.Fatalf("W1 regression: attacker row stored the unverified email hint %q (squatting)", *attackerEmail)
	}

	// Legit path still works: a repeat login for the same Privy id is idempotent —
	// same account, not a duplicate.
	gotPublic2, created2, err := repo.UpsertUserFromPrivy(ctx, identity.PrivyUpsertInput{
		PrivyUserID:  attackerPrivy,
		UserPublicID: "usr_w1_attacker_2",
		Profile:      identity.PrivyProfile{},
	})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if created2 {
		t.Fatal("second login for the same privy id created a duplicate account")
	}
	if gotPublic2 != gotPublic {
		t.Fatalf("second login resolved to a different account: %q vs %q", gotPublic2, gotPublic)
	}
}
