package devprofile

import "testing"

// The checklist is derived from account state, so the SAME developer gets the SAME
// answer on every device. The bug this replaces: completion was computed from
// localStorage, so a developer who filled everything in on their phone opened a laptop
// and saw 0% with a list telling them to redo finished work.
func TestCompletionIsDerivedFromAccountState(t *testing.T) {
	full := buildCompletion(CompletionState{
		DisplayName:   "Nightshade",
		AvatarURL:     "https://pyyol.com/v1/media/avatars/abc.jpg",
		Username:      "nightshade",
		WalletAddress: "7xKX…",
	})
	if !full.Complete || full.Percent != 100 {
		t.Fatalf("a fully populated profile reads %d%% (complete=%v)", full.Percent, full.Complete)
	}

	empty := buildCompletion(CompletionState{})
	// "verify" is done by construction (the route is authenticated), so a brand-new
	// developer is 1/5, not 0.
	if empty.Percent != 20 {
		t.Fatalf("a new profile reads %d%%, want 20 (identity verified, nothing else)", empty.Percent)
	}
	if empty.Complete {
		t.Fatal("an empty profile reported complete")
	}
}

// Each step tracks exactly one field. A step that ticks for the wrong reason is worse
// than one that never ticks: it tells the developer they are done when they are not.
func TestEachStepTracksItsOwnField(t *testing.T) {
	byKey := func(c Completion) map[string]bool {
		m := map[string]bool{}
		for _, s := range c.Steps {
			m[s.Key] = s.Done
		}
		return m
	}

	only := byKey(buildCompletion(CompletionState{AvatarURL: "https://x/y.jpg"}))
	if !only["avatar"] {
		t.Fatal("a stored avatar did not tick the avatar step")
	}
	for _, k := range []string{"name", "handle", "wallet"} {
		if only[k] {
			t.Fatalf("step %q ticked with nothing but an avatar set", k)
		}
	}

	wallet := byKey(buildCompletion(CompletionState{WalletAddress: "7xKX…"}))
	if !wallet["wallet"] {
		t.Fatal("a connected wallet did not tick the wallet step")
	}
}

// The list itself is the requirement, so it is pinned. Withdrawals and spending limits
// were removed deliberately: the first asked a developer to configure a payout rail
// before they had won anything, and the second is a guardrail with a working default —
// both left every new profile permanently short of 100%. "Connect your wallet" replaced
// them because it gates what a developer actually wants to do next.
func TestTheStepsAreTheOnesWeIntend(t *testing.T) {
	c := buildCompletion(CompletionState{})
	want := []string{"verify", "name", "avatar", "handle", "wallet"}
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

// Percent must never round up to 100 while a step is outstanding: "100% complete" with
// an unticked box is the kind of detail that makes a whole dashboard untrustworthy.
func TestPercentNeverRoundsUpToComplete(t *testing.T) {
	fourOfFive := buildCompletion(CompletionState{
		DisplayName: "n", AvatarURL: "a", Username: "u", // wallet missing
	})
	if fourOfFive.Percent >= 100 {
		t.Fatalf("4 of 5 steps reads %d%%", fourOfFive.Percent)
	}
	if fourOfFive.Complete {
		t.Fatal("4 of 5 steps reported complete")
	}
	if fourOfFive.Percent != 80 {
		t.Fatalf("4 of 5 steps reads %d%%, want 80", fourOfFive.Percent)
	}
}
