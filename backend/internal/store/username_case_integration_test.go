package store

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Username availability, against a REAL Postgres, because the defect this pins is
// invisible to any test that stubs the database.
//
// `users.username` is CITEXT and its UNIQUE index is too, so the DATABASE enforces
// uniqueness case-insensitively: with devA1 taken, an attempt to claim DEVA1 is
// rejected. The availability lookup used `username = $1`, and a bound parameter
// arrives typed `text` — which resolves to the case-SENSITIVE text operator. So the
// two disagreed:
//
//	availability: DEVA1 is free   →  green tick in the field
//	the index:    DEVA1 is taken  →  409 on save
//
// A user could not tell which was lying, and the same split is an impersonation
// vector: @Alice reading as available beside an existing @alice.
//
// This is behavioural on purpose. A structural test ("ResolveHandle contains a cast")
// would pass the moment someone reformats the query and keep passing if the cast were
// moved onto public_id, where it does not belong.
func TestUsernameResolutionIsCaseInsensitiveLikeTheUniqueIndex(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewDevProfileRepo(pool)

	// A username unlikely to collide with lab data, and mixed-case so every variant
	// below is genuinely a different byte string.
	const name = "CaseProbe7391x"
	_, publicID := seedUserForUsername(t, ctx, pool, name)

	for _, probe := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
		got, found, err := repo.ResolveHandle(ctx, probe)
		if err != nil {
			t.Fatalf("ResolveHandle(%q): %v", probe, err)
		}
		if !found {
			t.Fatalf("ResolveHandle(%q) = not found, but the unique index would REJECT "+
				"claiming it — availability and uniqueness must not disagree, or the field "+
				"shows a green tick and the save then fails", probe)
		}
		if got.UserPublicID != publicID {
			t.Fatalf("ResolveHandle(%q) resolved to %s, want %s", probe, got.UserPublicID, publicID)
		}
	}
}

// The cast must NOT have been applied to public_id, which is plain text and whose
// comparison is meant to stay exact. Widening it would let one developer's id resolve
// to another's row on case alone.
func TestPublicIDResolutionStaysCaseSensitive(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewDevProfileRepo(pool)

	const name = "CaseProbe7392x"
	_, publicID := seedUserForUsername(t, ctx, pool, name)

	if publicID == strings.ToUpper(publicID) {
		t.Skipf("public_id %q has no lowercase to flip; nothing to assert", publicID)
	}
	// The username is deliberately unrelated to the public id here, so a hit could
	// only come from the public_id arm having been widened.
	got, found, err := repo.ResolveHandle(ctx, strings.ToUpper(publicID))
	if err != nil {
		t.Fatalf("ResolveHandle: %v", err)
	}
	if found {
		t.Fatalf("upper-cased public_id %q resolved to %s — the public_id lookup must stay "+
			"exact, or ids collide on case alone", strings.ToUpper(publicID), got.UserPublicID)
	}
}

// Seeds a user holding `username`, and removes it afterwards so the shared lab
// database is left as it was found.
func seedUserForUsername(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string) (id, publicID string) {
	t.Helper()
	publicID = "usr_case_" + strings.ToLower(username)
	err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id, email, username)
		 VALUES ($1, $2, $3) RETURNING id`,
		publicID, strings.ToLower(username)+"@lab.invalid", username,
	).Scan(&id)
	if err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id); err != nil {
			t.Logf("cleanup user %s: %v", id, err)
		}
	})
	return id, publicID
}
