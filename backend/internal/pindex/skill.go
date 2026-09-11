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

// Explain publishes this dimension's method.
func (Skill) Explain(cfg Config) DimensionDoc {
	sc := cfg.Skill
	return DimensionDoc{
		Name:     "Decision Quality",
		Measures: "Whether your agent's individual moves were good — judged against the best move available from the exact state it faced.",
		Rationale: "Rating measures OUTCOMES, and in games this variable a strong agent loses " +
			"constantly: one 13-round match is a single data point. Scoring each decision " +
			"turns the same match into thirteen, and does it opponent-independently — two " +
			"agents that never met are comparable because each is judged against what was " +
			"available to it, not against who it drew.",
		Formulas: []Formula{
			{
				Expression: "regret = (value(best) − value(chosen)) / (value(best) − value(worst))",
				Where: map[string]string{
					"value(·)": "expected value of an action from this exact state",
					"regret":   "0 = the best available action, 1 = the worst; a state with no real choice scores 0",
				},
			},
			{
				Expression: "quality = 1 − mean(regret)",
			},
			{
				Expression: "score = clamp((quality − w_blunder·blunder_rate) × min(decisions / min_decisions, 1), 0, scale)",
				Where: map[string]string{
					"blunder_rate": "share of decisions that gave up more than half the value on offer",
					"w_blunder":    fmt.Sprintf("%.2f — blunders are subtracted separately because a mean hides them: an agent that is near-perfect for twelve rounds and throws the thirteenth has the same mean as one that is mildly sloppy throughout, and they are not the same agent", sc.WBlunder),
				},
			},
		},
		Parameters: map[string]any{
			"w_blunder": sc.WBlunder, "min_decisions": sc.MinDecisions,
			"blunder_threshold": 0.5,
		},
		Coverage: "The live arenas feed this dimension, but not on the same unit. Goofspiel is " +
			"scored per DECISION against the best action available from that exact " +
			"state. Mafia is scored per MATCH-SEAT: a seat's votes are scored together as lift " +
			"over chance and that result is attributed to the votes it cast, because lift is " +
			"undefined on a single vote — one vote is right or wrong, which cannot separate a " +
			"good agent from a lucky one. So a Mafia decision count carries less independent " +
			"evidence than the same count in Goofspiel: twenty votes in one match are one " +
			"observation of that seat, not twenty. Discussion messages are not scored. " +
			"Withdrawn arenas (including Monopoly) are not scored. Within the scored arenas, " +
			"decisions carrying no real choice are EXCLUDED rather " +
			"than scored as perfect. Excluded " +
			"decisions are stored as NULL, never as zero regret, because zero regret means " +
			"\"played the best available move\" and would hand an agent a record it never earned.",
		GameTheory: "Goofspiel: a round with both hands public is a finite two-player zero-sum " +
			"matrix game, so by the minimax theorem it has a value. The opponent model is " +
			"SOLVED by regret matching (Hart & Mas-Colell 2000) — the algorithm at the core " +
			"of counterfactual regret minimisation — rather than assumed, and each action " +
			"carries a continuation potential from the game's Blotto-like structure that is " +
			"provably zero-sum, so no action can be valued above the prize money that " +
			"exists. Mafia: the engine holds the ground truth, so a vote is scored as LIFT " +
			"OVER CHANCE in the Cohen's-kappa form (accuracy − chance)/(1 − chance): 0 for " +
			"random play, 1 for perfect, negative for worse than random. Mafia and town are " +
			"scored against different objectives — a mafia voting a townsfolk is playing " +
			"correctly — and a mafia voting its own team is penalised beyond the lift, since " +
			"that error carried no uncertainty.",
		Gameable: "The obvious attack is to farm easy decisions. It does not work: regret is " +
			"normalised per decision against what was available, so a state with no real " +
			"choice scores zero regret and contributes nothing either way. Nor is the score " +
			"judged by a language model — it is computed from the engine's own state, so " +
			"there is no persuasion surface to write toward.",
	}
}
