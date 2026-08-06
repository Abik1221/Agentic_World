package pindex

import "fmt"

const keySkill = "skill"

// Skill is DECISION QUALITY — the dimension that asks whether the moves were good.
//
// The four original dimensions between them measure results and hygiene:
//
//   - Arena, Consistency and Difficulty are all functions of RATING, which is a function
//     of who WON. In games this variable, a strong agent loses constantly.
//   - Intelligence, despite the name, measures conduct: legal-move rate, fallback rate,
//     latency. An agent that plays fast, legal, terrible moves scores full marks on it.
//
// Nothing scored whether a move was any good. This does, using internal/skill: every
// decision is compared against the best action available from that exact state, computed
// deterministically from the engine's own ground truth. No model in the loop.
//
// Why it belongs in the P-Index rather than replacing rating: they answer different
// questions. Rating decides who gets PAID — that is the game, variance and all. This
// decides who ranks, and it does so on 13-50 observations per match instead of one.
//
// # Shipping at weight zero, on purpose
//
// The P-Index is public and drives reputation. Turning on a new dimension with a real
// weight would silently re-rank every developer on the platform overnight, using a
// scorer that has never been measured against live play. So the default weight is 0: the
// score is computed, stored and shown, and contributes nothing until an operator sets a
// weight deliberately. Measure first, then weight — the same discipline the ranked
// integrity gate shipped under.
type Skill struct{}

func (Skill) Key() string { return keySkill }

func (Skill) Score(in DeveloperInputs, cfg Config) SubScore {
	sc := cfg.Skill
	if in.SkillDecisions <= 0 {
		return SubScore{Key: keySkill, Score: 0, Reason: "no scored decisions yet"}
	}

	quality := clamp(in.SkillQuality, 0, 1)

	// Sample-size gate, for the same reason Intelligence has one: decision quality has
	// real variance, and three brilliant decisions are not evidence of a brilliant agent.
	// Partial credit scales linearly to MinDecisions so the score grows with the evidence
	// rather than jumping.
	gate := 1.0
	if sc.MinDecisions > 0 {
		gate = clamp(float64(in.SkillDecisions)/float64(sc.MinDecisions), 0, 1)
	}

	// Blunders are penalised separately from mean quality because the mean hides them.
	// An agent that is near-perfect for twelve rounds and throws the thirteenth has the
	// same mean as one that is mildly sloppy throughout, and they are not the same agent
	// — the first one loses matches it had won.
	blunderPenalty := clamp(in.SkillBlunderRate, 0, 1) * sc.WBlunder

	raw := clamp(quality-blunderPenalty, 0, 1)
	score := clamp(raw*gate*cfg.Scale, 0, cfg.Scale)

	return SubScore{
		Key:   keySkill,
		Score: score,
		Reason: fmt.Sprintf("decision quality %.0f%% over %d scored decisions, %.0f%% blunders",
			quality*100, in.SkillDecisions, in.SkillBlunderRate*100),
	}
}
