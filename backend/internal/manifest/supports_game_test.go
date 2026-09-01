package manifest

import (
	"context"
	"testing"
)

// SupportsGame drives the ranked-entry game gate: it must distinguish "declares a
// different game" (found && !supported) from "no manifest yet" (!found), so the
// enqueue gate can hard-reject a Mafia-only agent from the Goofspiel queue while the
// enable-time gate stays quiet when it can't yet tell.
func TestSupportsGame(t *testing.T) {
	ctx := context.Background()

	// Active manifest declaring goofspiel ONLY, so "mafia" exercises the
	// "declares a different game" branch (found, not supported).
	svc := New(&fakeRepo{
		active:      Manifest{AgentPublicID: "ag", Games: []string{"goofspiel"}},
		activeFound: true,
	}, nil, nil)

	if sup, found, err := svc.SupportsGame(ctx, "ag", "goofspiel"); err != nil || !sup || !found {
		t.Fatalf("goofspiel: want supported+found, got sup=%v found=%v err=%v", sup, found, err)
	}
	if sup, found, err := svc.SupportsGame(ctx, "ag", "mafia"); err != nil || sup || !found {
		t.Fatalf("mafia: want not-supported but found, got sup=%v found=%v err=%v", sup, found, err)
	}

	// No active manifest at all ⇒ can't determine.
	none := New(&fakeRepo{activeFound: false}, nil, nil)
	if sup, found, err := none.SupportsGame(ctx, "ag", "goofspiel"); err != nil || sup || found {
		t.Fatalf("no manifest: want (false,false,nil), got sup=%v found=%v err=%v", sup, found, err)
	}
}
