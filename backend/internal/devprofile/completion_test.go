package devprofile

import "testing"

// The checklist is derived from account state, so the SAME developer gets the SAME
// answer on every device. The bug this replaces: completion was computed from
// localStorage, so a developer who filled everything in on their phone opened a laptop
// and saw 0% with a list telling them to redo finished work.
func TestCompletionIsDerivedFromAccountState(t *testing.T) {
	full := buildCompletion(CompletionState{
		DisplayName:    "Nightshade",
		AvatarURL:      "https://pyyol.com/v1/media/avatars/abc.jpg",
		Username:       "nightshade",
		WalletAddress:  "7xKX…",
		VerifiedWallet: "7xKX…",
	})
	if !full.Complete || full.Percent != 100 {
		t.Fatalf("a fully populated profile reads %d%% (complete=%v)", full.Percent, full.Complete)
	}

	empty := buildCompletion(CompletionState{})
	// "verify" is done by construction (the route is authenticated), so a brand-new
	// developer is 1/6, not 0.
	if empty.Percent != 16 {
		t.Fatalf("a new profile reads %d%%, want 16 (identity verified, nothing else)", empty.Percent)
	}
	if empty.Complete {
		t.Fatal("an empty profile reported complete")
	}
}

// Each step tracks exactly one field. A step that ticks for the wrong reason is worse
// than one that never ticks: it tells the developer they are done when they are not.
func TestEachStepTracksItsOwnField(t *testing.T) {
	only := stepsByKey(buildCompletion(CompletionState{AvatarURL: "https://x/y.jpg"}))
	if !only["avatar"] {
		t.Fatal("a stored avatar did not tick the avatar step")
	}
	for _, k := range []string{"name", "handle", "wallet", "wallet_verified"} {
		if only[k] {
			t.Fatalf("step %q ticked with nothing but an avatar set", k)
		}
	}
}

// CONNECTING IS NOT VERIFYING. A connected wallet shares a public address; a verified one
// has proven ownership by signature and is the only thing that lets a payout happen.
//
// They were a single step satisfied by either column, which understated the remaining
// work: a developer read 100% complete and then met a signature request at the moment they
// tried to cash out — the worst possible time to discover an unfinished step.
func TestConnectingAWalletDoesNotTickVerification(t *testing.T) {
	connected := stepsByKey(buildCompletion(CompletionState{WalletAddress: "7xKX…"}))
	if !connected["wallet"] {
		t.Fatal("a connected wallet did not tick the connect step")
	}
	if connected["wallet_verified"] {
		t.Fatal("connecting a wallet ticked VERIFICATION — a payout would still be refused")
	}
}

// A proven wallet implies a connected one. The hint column is empty for accounts that
// verified before the connect-recording endpoint shipped, and showing those developers an
// unticked "connect" beneath a ticked "verify" would be nonsense.
func TestVerifiedWalletImpliesConnected(t *testing.T) {
	verifiedOnly := stepsByKey(buildCompletion(CompletionState{VerifiedWallet: "7xKX…"}))
	if !verifiedOnly["wallet_verified"] {
		t.Fatal("a proven wallet did not tick the verification step")
	}
	if !verifiedOnly["wallet"] {
		t.Fatal("a proven wallet left 'connect your wallet' unticked — it cannot be undone")
	}
}

// The list itself is the requirement, so it is pinned.
//
// "Set up withdrawals" and "Set spending limits" are gone for good: neither was real
// account state (both were localStorage booleans the developer ticked by hand, so they
// lied on a second device), the first demanded a payout rail before anyone had won
// anything, and the second turned a guardrail with a working default into a chore.
func TestTheStepsAreTheOnesWeIntend(t *testing.T) {
	c := buildCompletion(CompletionState{})
	want := []string{"verify", "name", "avatar", "handle", "wallet", "wallet_verified"}
	if len(c.Steps) != len(want) {
		t.Fatalf("checklist has %d steps, want %d", len(c.Steps), len(want))
	}
	for i, k := range want {
		if c.Steps[i].Key != k {
			t.Fatalf("step %d = %q, want %q", i, c.Steps[i].Key, k)
		}
	}
	for _, s := range c.Steps {
		if s.Key == "withdrawal" || s.Key == "limits" {
			t.Fatalf("%q is back on the checklist", s.Key)
		}
		if s.Label == "" || s.Href == "" || s.CTA == "" {
			t.Fatalf("step %q is missing label/href/cta — the client renders these", s.Key)
		}
	}
}

// No step may ask for spending limits or a payout rail, however it is worded. Pinned on
// the LABELS too, because the bug the developer sees is the sentence, not the key.
func TestNoStepAsksForLimitsOrPayoutSetup(t *testing.T) {
	for _, s := range buildCompletion(CompletionState{}).Steps {
		for _, banned := range []string{"limit", "payout setup", "set up withdrawal"} {
			if containsFold(s.Label, banned) {
				t.Fatalf("step %q says %q — that requirement was removed", s.Key, s.Label)
			}
		}
	}
}

// Percent must never round up to 100 while a step is outstanding: "100% complete" with
// an unticked box is the kind of detail that makes a whole dashboard untrustworthy.
func TestPercentNeverRoundsUpToComplete(t *testing.T) {
	fiveOfSix := buildCompletion(CompletionState{
		DisplayName: "n", AvatarURL: "a", Username: "u", WalletAddress: "w", // unverified
	})
	if fiveOfSix.Percent >= 100 {
		t.Fatalf("5 of 6 steps reads %d%%", fiveOfSix.Percent)
	}
	if fiveOfSix.Complete {
		t.Fatal("5 of 6 steps reported complete")
	}
	if fiveOfSix.Percent != 83 {
		t.Fatalf("5 of 6 steps reads %d%%, want 83", fiveOfSix.Percent)
	}
}

func stepsByKey(c Completion) map[string]bool {
	m := map[string]bool{}
	for _, s := range c.Steps {
		m[s.Key] = s.Done
	}
	return m
}

// containsFold is a tiny case-insensitive Contains, kept local so the test file needs no
// import beyond testing.
func containsFold(haystack, needle string) bool {
	lower := func(s string) string {
		b := []byte(s)
		for i := range b {
			if b[i] >= 'A' && b[i] <= 'Z' {
				b[i] += 'a' - 'A'
			}
		}
		return string(b)
	}
	h, n := lower(haystack), lower(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
