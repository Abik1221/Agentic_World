package match

import (
	"encoding/json"
	"testing"

	"github.com/agent-arena/arena/internal/turnproof"
)

// The token must actually reach the agent, and must be the one the gateway will
// accept for exactly this turn. If the view ships without it, every honest agent
// measures 0% LLM-backed and enforcement would void real matches.
func TestTurnViewCarriesAProofTheGatewayAccepts(t *testing.T) {
	sig := turnproof.New("secret")
	d := &driver{turns: sig}

	tok := d.mintProof("ag_1", "m_1", 7)
	if tok == "" {
		t.Fatal("no proof minted for the turn")
	}
	if !sig.Verify("ag_1", "m_1", 7, tok) {
		t.Fatal("the gateway would reject the token the platform just issued")
	}
	// And it is useless for any other turn — the point of binding.
	if sig.Verify("ag_1", "m_1", 8, tok) {
		t.Fatal("round 7's proof verified for round 8")
	}
}

// No minter configured must ship no token rather than panic or send a placeholder a
// gateway might accept.
func TestNoMinterShipsNoProof(t *testing.T) {
	if got := (&driver{}).mintProof("ag_1", "m_1", 1); got != "" {
		t.Fatalf("expected no proof, got %q", got)
	}
}

// The field must serialise under the name the SDK reads, and must vanish when empty
// so older agents see no unexplained key.
func TestProofSerialisesAsTurnProofAndOmitsWhenEmpty(t *testing.T) {
	b, err := json.Marshal(goofspielTurnView{TurnProof: "tok-abc"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m["turn_proof"] != "tok-abc" {
		t.Fatalf("turn_proof missing or renamed: %v", m["turn_proof"])
	}

	b2, _ := json.Marshal(goofspielTurnView{})
	var m2 map[string]any
	_ = json.Unmarshal(b2, &m2)
	if _, present := m2["turn_proof"]; present {
		t.Fatal("empty turn_proof should be omitted, not sent as \"\"")
	}
}

// EnableRankedDrive copies the minter onto the driver, so installing it afterwards
// silently ships views with no token. Pin the ordering that actually works.
func TestMinterMustBeInstalledBeforeEnablingDrive(t *testing.T) {
	s := &Service{}
	s.SetTurnMinter(turnproof.New("secret"))
	s.driver = &driver{turns: s.turns} // what EnableRankedDrive does

	if s.driver.mintProof("ag_1", "m_1", 1) == "" {
		t.Fatal("minter set before drive was enabled but no proof is issued")
	}
}
