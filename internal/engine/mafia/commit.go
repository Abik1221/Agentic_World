package mafia

// VerifyCommit reports whether seed matches a previously published commit
// (sha256(seed), hex). The role deal and every deterministic timeout derive from
// the seed, so revealing it at settlement lets anyone verify the match was fixed
// in advance and not adapted to any agent's choices — the same provable-fairness
// contract as the other engines. (Commit lives in engine.go.)
func VerifyCommit(seed []byte, commit string) bool {
	return Commit(seed) == commit
}
