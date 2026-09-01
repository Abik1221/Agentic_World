package antifraud

// Same-beneficiary linkage: the multi-account hole.
//
// # What was wrong
//
// Matchmaking refused to seat two agents together when they shared an OwnerPublicID, and
// nothing else. An audit put it in one line: two accounts defeat it entirely. Register twice
// and you are matched against yourself, and every detector downstream is measuring a ring it
// was never allowed to see — a pair that is never seated together produces no co-voted
// rounds, no shared trades and no coin flow.
//
// # Why beneficiary rather than device
//
// The obvious fix is device or IP fingerprinting. We do not collect either, and I would not
// reach for it first even if we did: it is trivially defeated by a second browser, it
// misfires on shared networks — a university, an office, a co-working space full of exactly
// the developers we want — and it identifies a MACHINE when the thing we care about is a
// PERSON getting paid.
//
// A ring exists to move money to one place. So link accounts by where the money GOES:
//
//   - the same payout destination wallet
//   - the same Stripe Connect account
//   - the same login wallet address
//
// Two accounts withdrawing to one wallet are one beneficiary whatever their signup looked
// like, and unlike a device fingerprint that link is the thing the fraud is FOR. An attacker
// can avoid it only by genuinely splitting the proceeds to separate destinations, which is
// most of the way to not being a ring.
//
// # Why this is a matchmaking guard, not a ban
//
// Sharing a payout destination is not proof of wrongdoing. Two developers at one company, a
// person running a second agent honestly, a shared custodial wallet — all legitimate, all
// linked. So the consequence is that they are NOT SEATED TOGETHER at a staked table, which
// costs an honest pair nothing and removes the ring's mechanism entirely. Refusing the seat
// is cheap and reversible; a ban is neither.
//
// # The residual hole, stated plainly
//
// An attacker who withdraws to two genuinely separate wallets, from two Stripe accounts,
// is not linked by this and will be seated. What catches them is the downstream behaviour —
// coin flow, vote agreement, transfer asymmetry — which is exactly why those had to exist
// first. This closes the cheap attack so the expensive detectors are worth running.

import "sort"

// Beneficiary is a payout identity shared across accounts.
//
// Kind is recorded alongside the value so a reviewer sees WHY two accounts were linked. A
// bare "these are linked" is unactionable; "both withdraw to this wallet" is a decision a
// human can make in seconds.
type Beneficiary struct {
	Kind  string // "payout_wallet" | "stripe_connect" | "login_wallet"
	Value string
}

// LinkedGroup is a set of owner public ids that share a beneficiary.
type LinkedGroup struct {
	Owners []string
	Via    Beneficiary
}

// Kinds of beneficiary link, strongest first.
//
// Payout destination is strongest because it is where the money actually lands and an
// attacker cannot fake sharing it — they either receive at one address or they do not.
// A login wallet is weakest: custodial and embedded wallets can legitimately collide, and
// it is included so a reviewer sees the weak signal rather than so it can gate anything on
// its own.
const (
	LinkPayoutWallet  = "payout_wallet"
	LinkStripeConnect = "stripe_connect"
	LinkLoginWallet   = "login_wallet"
)

// LinkIndex answers "may these two owners be seated together?".
//
// Built once per matchmaking sweep and consulted per candidate pair, because the alternative
// is a database round trip inside the pairing loop and pairing sits on the hot path of every
// queued match.
type LinkIndex struct {
	// group maps an owner public id to the set of owners it is linked to, transitively.
	group map[string]map[string]bool
}

// NewLinkIndex builds the index from discovered groups.
//
// Linkage is TRANSITIVE: if A and B share a wallet and B and C share a Stripe account, then
// A and C are one beneficiary even though they share nothing directly. Treating links as
// pairwise would let a ring launder the relationship through a middle account, which is the
// first thing anyone would try.
func NewLinkIndex(groups []LinkedGroup) *LinkIndex {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		root := find(parent[x])
		parent[x] = root // path compression
		return root
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}
	for _, g := range groups {
		for i := 1; i < len(g.Owners); i++ {
			union(g.Owners[0], g.Owners[i])
		}
	}
	idx := &LinkIndex{group: map[string]map[string]bool{}}
	for owner := range parent {
		root := find(owner)
		if idx.group[root] == nil {
			idx.group[root] = map[string]bool{}
		}
		idx.group[root][owner] = true
	}
	// Re-key by member so lookup is O(1) per owner rather than a search for the root.
	byMember := map[string]map[string]bool{}
	for _, members := range idx.group {
		for m := range members {
			byMember[m] = members
		}
	}
	idx.group = byMember
	return idx
}

// Linked reports whether two owners are the same beneficiary.
//
// An owner is always linked to itself, so this subsumes the old OwnerPublicID check rather
// than sitting beside it. One predicate, so a caller cannot apply half the rule.
func (l *LinkIndex) Linked(ownerA, ownerB string) bool {
	if ownerA == "" || ownerB == "" {
		// An unidentified owner is not evidence of a link. Returning true here would refuse
		// to seat anyone whose owner id failed to load, which turns a lookup glitch into a
		// platform-wide outage of matchmaking.
		return false
	}
	if ownerA == ownerB {
		return true
	}
	if l == nil {
		return false
	}
	members, ok := l.group[ownerA]
	return ok && members[ownerB]
}

// GroupOf returns everyone sharing a beneficiary with this owner, sorted, including itself.
// For a reviewer looking at a flag.
func (l *LinkIndex) GroupOf(owner string) []string {
	if l == nil || owner == "" {
		return nil
	}
	members, ok := l.group[owner]
	if !ok {
		return []string{owner}
	}
	out := make([]string, 0, len(members))
	for m := range members {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Size is how many owners are in any link group. Zero means no linkage was found at all,
// which on a young platform is the normal case and should not be read as "the check ran and
// found nothing" without also checking that the query returned rows.
func (l *LinkIndex) Size() int {
	if l == nil {
		return 0
	}
	return len(l.group)
}
