package devprofile

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stubRepo answers only what the username paths need: which handles already exist.
type unameRepo struct {
	taken map[string]bool
	Repo  // embedded so the unused methods are nil — these tests never call them
}

func (r unameRepo) ResolveHandle(_ context.Context, h string) (Identity, bool, error) {
	if r.taken[strings.ToLower(h)] {
		return Identity{}, true, nil
	}
	return Identity{}, false, nil
}

func svcWith(taken ...string) *Service {
	m := map[string]bool{}
	for _, t := range taken {
		m[strings.ToLower(t)] = true
	}
	return &Service{repo: unameRepo{taken: m}}
}

// The field must reject exactly what the save rejects. A green tick followed by a
// failed submit is the single most annoying failure this feature can have.
func TestLiveCheckAgreesWithTheWritePath(t *testing.T) {
	for _, name := range []string{"ab", "has space", "bad-dash", "usr_1234", "admin", "pyyol", strings.Repeat("x", 31)} {
		st, err := svcWith().CheckUsername(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		if st.Available {
			t.Fatalf("%q reported available by the live check", name)
		}
		if st.Reason == "" {
			t.Fatalf("%q rejected with no reason to show the user", name)
		}
		// The write path must refuse it too.
		if validateUsernameShape(strings.TrimPrefix(name, "@")) == "" {
			t.Fatalf("%q passes the write validator but the live check refused it", name)
		}
	}
}

func TestTakenNamesAreNotAvailable(t *testing.T) {
	s := svcWith("nahom")
	st, _ := s.CheckUsername(context.Background(), "nahom")
	if st.Available {
		t.Fatal("an existing handle reported as free")
	}
	// Case must not be a loophole — handles resolve case-insensitively.
	st, _ = s.CheckUsername(context.Background(), "NAHOM")
	if st.Available {
		t.Fatal("case variation of a taken handle reported as free")
	}
}

func TestGoodNameIsAvailable(t *testing.T) {
	st, err := svcWith("someoneelse").CheckUsername(context.Background(), "@nahom_t2")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Available {
		t.Fatalf("valid free name refused: %q", st.Reason)
	}
	if st.Username != "nahom_t2" {
		t.Fatalf("leading @ was not stripped: %q", st.Username)
	}
}

// Reserved names are about impersonation, not aesthetics: @support asking for your
// seed phrase is the expensive version of this bug.
func TestImpersonationProneNamesAreReserved(t *testing.T) {
	for _, n := range []string{"admin", "Support", "BILLING", "moderator", "pyyol", "security"} {
		if reason := validateUsernameShape(n); reason == "" {
			t.Fatalf("%q was allowed", n)
		}
	}
}

// Someone who skips the field still gets a real handle — never a raw database id.
func TestSuggestionPrefersDisplayNameThenEmail(t *testing.T) {
	s := svcWith()
	got, err := s.SuggestUsername(context.Background(), "Nahom T.", "someone@pyyol.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomt" {
		t.Fatalf("got %q, want %q from the display name", got, "nahomt")
	}

	// No usable display name → fall back to the email local part.
	got, err = s.SuggestUsername(context.Background(), "!!!", "nahom.dev@gmail.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomdev" {
		t.Fatalf("got %q, want %q from the email", got, "nahomdev")
	}
}

func TestSuggestionAvoidsCollisions(t *testing.T) {
	s := svcWith("nahomt", "nahomt2")
	got, err := s.SuggestUsername(context.Background(), "Nahom T", "n@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "nahomt3" {
		t.Fatalf("got %q, want nahomt3 — the first two are taken", got)
	}
}

// A suggestion that trips the reserved list or the shape rules would be handed to the
// user as a green tick and then rejected on save.
func TestSuggestionNeverProducesAnInvalidName(t *testing.T) {
	s := svcWith()
	for _, in := range [][2]string{
		{"admin", "admin@x.com"},             // reserved base
		{"", ""},                             // nothing to work with
		{"!!!", "!!!@x.com"},                 // nothing usable
		{"a", "b@x.com"},                     // too short
		{strings.Repeat("z", 60), "c@x.com"}, // too long
	} {
		got, err := s.SuggestUsername(context.Background(), in[0], in[1])
		if err != nil {
			t.Fatalf("no suggestion for %q/%q: %v", in[0], in[1], err)
		}
		if reason := validateUsernameShape(got); reason != "" {
			t.Fatalf("suggested %q which the validator rejects: %s", got, reason)
		}
	}
}

// ─────────────────────── the Bloom fast path ───────────────────────────────
//
// The availability check backs a live flag under a text input on a PUBLIC,
// unauthenticated, unthrottled route. The point of the filter is that the common
// answer — "that name is free" — costs no database round trip. These tests pin both
// halves: the lookup is skipped when it can be, and still happens when correctness
// needs it.

// countingRepo records how many times the uniqueness lookup actually ran.
type countingRepo struct {
	taken map[string]bool
	hits  int
	Repo  // unused methods stay nil
}

func (r *countingRepo) ResolveHandle(_ context.Context, h string) (Identity, bool, error) {
	r.hits++
	if r.taken[strings.ToLower(h)] {
		return Identity{}, true, nil
	}
	return Identity{}, false, nil
}

func (r *countingRepo) AllUsernames(context.Context) ([]string, error) {
	out := make([]string, 0, len(r.taken))
	for t := range r.taken {
		out = append(out, t)
	}
	return out, nil
}

func TestAvailableNameIsAnsweredWithoutTouchingTheDatabase(t *testing.T) {
	repo := &countingRepo{taken: map[string]bool{"alice": true, "bob": true}}
	s := &Service{repo: repo}
	if err := s.RefreshUsernameFilter(context.Background()); err != nil {
		t.Fatalf("RefreshUsernameFilter: %v", err)
	}
	repo.hits = 0

	for i := 0; i < 20; i++ {
		st, err := s.CheckUsername(context.Background(), fmt.Sprintf("free_name_%d", i))
		if err != nil {
			t.Fatalf("CheckUsername: %v", err)
		}
		if !st.Available {
			t.Fatalf("free_name_%d reported unavailable: %q", i, st.Reason)
		}
	}
	if repo.hits > 3 {
		t.Fatalf("%d database lookups for 20 free names — the Bloom fast path is not "+
			"engaging, so a public unthrottled endpoint still costs a query per keystroke",
			repo.hits)
	}
}

func TestTakenNameStillConsultsTheDatabase(t *testing.T) {
	repo := &countingRepo{taken: map[string]bool{"alice": true}}
	s := &Service{repo: repo}
	if err := s.RefreshUsernameFilter(context.Background()); err != nil {
		t.Fatalf("RefreshUsernameFilter: %v", err)
	}
	repo.hits = 0

	st, err := s.CheckUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("CheckUsername: %v", err)
	}
	if st.Available {
		t.Fatal("a claimed username was reported available — the filter must never skip " +
			"the lookup that settles a positive")
	}
	if repo.hits == 0 {
		t.Fatal("no lookup ran for a name the filter flagged; a Bloom hit is only ever " +
			"'probably present' and has to be confirmed")
	}
}

// users.username is CITEXT, so the database matches case-insensitively. A filter built
// lowercased must be probed lowercased, or "Alice" reads as free while the database
// considers it taken.
func TestFilterMatchesCitextCaseInsensitivity(t *testing.T) {
	repo := &countingRepo{taken: map[string]bool{"alice": true}}
	s := &Service{repo: repo}
	if err := s.RefreshUsernameFilter(context.Background()); err != nil {
		t.Fatalf("RefreshUsernameFilter: %v", err)
	}
	for _, probe := range []string{"Alice", "ALICE", "aLiCe"} {
		st, err := s.CheckUsername(context.Background(), probe)
		if err != nil {
			t.Fatalf("CheckUsername(%q): %v", probe, err)
		}
		if st.Available {
			t.Fatalf("%q reported available, but the database holds 'alice' in a citext "+
				"column and would match it", probe)
		}
	}
}

func TestClaimingWritesThroughToTheFilter(t *testing.T) {
	repo := &countingRepo{taken: map[string]bool{}}
	s := &Service{repo: repo}
	if err := s.RefreshUsernameFilter(context.Background()); err != nil {
		t.Fatalf("RefreshUsernameFilter: %v", err)
	}
	if st, _ := s.CheckUsername(context.Background(), "newcomer"); !st.Available {
		t.Fatal("name was not available before being claimed")
	}
	s.noteUsernameClaimed("newcomer")
	repo.taken["newcomer"] = true

	st, err := s.CheckUsername(context.Background(), "newcomer")
	if err != nil {
		t.Fatalf("CheckUsername: %v", err)
	}
	if st.Available {
		t.Fatal("a just-claimed name was still offered — write-through is not working, so " +
			"the field would stay green until the next rebuild")
	}
}

// Cold start: before the first rebuild there is no filter, and the check must fall
// through to the database rather than declare everything free.
func TestNoFilterYetFallsThroughToTheDatabase(t *testing.T) {
	repo := &countingRepo{taken: map[string]bool{"alice": true}}
	s := &Service{repo: repo} // deliberately NOT refreshed
	st, err := s.CheckUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("CheckUsername: %v", err)
	}
	if st.Available {
		t.Fatal("with no filter built, a taken name was reported available — a cold start " +
			"must be safe, not optimistic")
	}
	if repo.hits == 0 {
		t.Fatal("no lookup ran on a cold start")
	}
}
