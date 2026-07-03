package monopoly

import "testing"

func TestBoardIntegrity(t *testing.T) {
	b := Board()
	if len(b) != BoardSize {
		t.Fatalf("board has %d spaces, want %d", len(b), BoardSize)
	}
	if b[IdxGo].Kind != KindGo || b[IdxJail].Kind != KindJail ||
		b[IdxFreeParkng].Kind != KindFreeParking || b[IdxGoToJail].Kind != KindGoToJail {
		t.Fatal("fixed corner squares are misplaced")
	}
	// Indices must be self-consistent.
	for i, sp := range b {
		if sp.Index != i {
			t.Fatalf("space %d reports index %d", i, sp.Index)
		}
	}
}

func TestGroupCounts(t *testing.T) {
	want := map[string]int{
		GroupBrown: 2, GroupLightBlue: 3, GroupPink: 3, GroupOrange: 3,
		GroupRed: 3, GroupYellow: 3, GroupGreen: 3, GroupDarkBlue: 2,
		GroupRailroad: 4, GroupUtility: 2,
	}
	for g, n := range want {
		if got := len(groupMembers[g]); got != n {
			t.Errorf("group %s has %d members, want %d", g, got, n)
		}
	}
	// Every ownable square belongs to exactly one known group.
	total := 0
	for _, n := range want {
		total += n
	}
	owned := 0
	for _, sp := range boardData {
		if sp.Ownable() {
			owned++
		}
	}
	if owned != total {
		t.Fatalf("ownable squares (%d) != grouped squares (%d)", owned, total)
	}
}

func TestKnownPricesAndRents(t *testing.T) {
	if space(1).Price != 60 || space(39).Price != 400 {
		t.Fatal("Mediterranean/Boardwalk prices wrong")
	}
	if space(39).Rent[5] != 2000 {
		t.Fatalf("Boardwalk hotel rent = %d, want 2000", space(39).Rent[5])
	}
	if space(5).Kind != KindRailroad || space(12).Kind != KindUtility {
		t.Fatal("railroad/utility kinds wrong")
	}
	if space(4).Tax != 200 || space(38).Tax != 100 {
		t.Fatalf("tax squares wrong: %d %d", space(4).Tax, space(38).Tax)
	}
}
