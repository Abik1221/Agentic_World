package rating

import "testing"

// Classification is what makes a brand-new model join every comparison on its first
// match. These pin the cases where getting it wrong would credit the wrong vendor or
// put a model in the wrong camp.
func TestClassify(t *testing.T) {
	cases := []struct {
		name             string
		provider, model  string
		vendor, family   string
		openness, hosted string
	}{
		{
			name:     "openai frontier",
			provider: "openai", model: "gpt-4o-2024-08-06",
			vendor: "openai", family: "gpt-4o", openness: Proprietary, hosted: HostedAPI,
		},
		{
			name:     "anthropic by tier, not just by brand",
			provider: "anthropic", model: "claude-opus-4-1-20250805",
			vendor: "anthropic", family: "claude-opus", openness: Proprietary, hosted: HostedAPI,
		},
		{
			// The case a naive board gets wrong: Groq SERVES it, Meta BUILT it. Filing
			// this under "groq" as a vendor would credit the wrong company for the
			// model's play, which is the whole reason provider and vendor are separate.
			name:     "hosted open-weights: served by one company, built by another",
			provider: "groq", model: "llama-3.3-70b-versatile",
			vendor: "meta", family: "llama-3", openness: OpenWeights, hosted: HostedAPI,
		},
		{
			name:     "self-hosted via ollama, with a tag",
			provider: "ollama", model: "llama3.3:70b-instruct-q4_K_M",
			vendor: "meta", family: "llama-3", openness: OpenWeights, hosted: SelfHosted,
		},
		{
			name:     "openrouter routing prefix is stripped",
			provider: "openrouter", model: "meta-llama/llama-3.1-8b-instruct",
			vendor: "meta", family: "llama-3", openness: OpenWeights, hosted: HostedAPI,
		},
		{
			name:     "bedrock region prefix is stripped",
			provider: "bedrock", model: "us.anthropic.claude-sonnet-4-20250514-v1:0",
			vendor: "anthropic", family: "claude-sonnet", openness: Proprietary, hosted: HostedAPI,
		},
		{
			// Same vendor, different openness — a vendor grouping alone would lose this.
			name:     "an open-weight model from a proprietary vendor",
			provider: "ollama", model: "gpt-oss-20b",
			vendor: "openai", family: "gpt-oss", openness: OpenWeights, hosted: SelfHosted,
		},
		{
			name:     "gemma is open, gemini is not",
			provider: "google", model: "gemma-2-27b",
			vendor: "google", family: "gemma", openness: OpenWeights, hosted: HostedAPI,
		},
		{
			name:     "gemini stays proprietary",
			provider: "google", model: "gemini-2.5-pro",
			vendor: "google", family: "gemini", openness: Proprietary, hosted: HostedAPI,
		},
		{
			// A model no rule knows, self-hosted. It must still land in the open-weights
			// comparison: you cannot self-host weights you were never given.
			name:     "unknown model on a local runtime is still open-weights",
			provider: "vllm", model: "some-brand-new-model-v7",
			vendor: "unknown", family: "unknown", openness: OpenWeights, hosted: SelfHosted,
		},
		{
			// A model no rule knows, on no known provider. Everything unknown — it is
			// NEVER guessed into a vendor's bucket.
			name:     "wholly unknown stays unknown",
			provider: "", model: "mystery-model",
			vendor: "unknown", family: "unknown", openness: OpennessUnknown, hosted: HostingUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.provider, c.model)
			if got.Vendor != c.vendor {
				t.Errorf("vendor = %q, want %q", got.Vendor, c.vendor)
			}
			if got.Family != c.family {
				t.Errorf("family = %q, want %q", got.Family, c.family)
			}
			if got.Openness != c.openness {
				t.Errorf("openness = %q, want %q", got.Openness, c.openness)
			}
			if got.Hosting != c.hosted {
				t.Errorf("hosting = %q, want %q", got.Hosting, c.hosted)
			}
		})
	}
}

// "gpt-4.1" must not be mangled by the Bedrock dotted-prefix stripping.
func TestNormalizeModelKeepsVersionDots(t *testing.T) {
	if got := normalizeModel("gpt-4.1-mini"); got != "gpt-4.1-mini" {
		t.Fatalf("normalizeModel = %q, want the version dots intact", got)
	}
	if c := Classify("openai", "gpt-4.1-mini"); c.Family != "gpt-4.1" {
		t.Fatalf("family = %q, want gpt-4.1", c.Family)
	}
}

// Groups pool RAW counters. Averaging per-model rates instead would let a model with
// four games move a group as much as one with four hundred.
func TestBuildGroupsPoolsCountersNotRates(t *testing.T) {
	models := []ModelStat{
		// Two Meta models on different hosts, one dominant and one terrible.
		{Provider: "groq", Model: "llama-3.3-70b", Matches: 100, Wins: 90, Losses: 10,
			Decisions: 1000, Legal: 1000, Tokens: 100_000, EstCostUSD: 10},
		{Provider: "ollama", Model: "llama-3.1-8b", Matches: 4, Wins: 0, Losses: 4,
			Decisions: 40, Legal: 20, Tokens: 4_000},
		{Provider: "openai", Model: "gpt-4o", Matches: 50, Wins: 25, Losses: 25,
			Decisions: 500, Legal: 500, Tokens: 50_000, EstCostUSD: 20},
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	groups := BuildGroups(models)

	find := func(kind, key string) *GroupStat {
		for i := range groups {
			if groups[i].Kind == kind && groups[i].Key == key {
				return &groups[i]
			}
		}
		return nil
	}

	meta := find(GroupVendor, "meta")
	if meta == nil {
		t.Fatalf("no meta vendor group; got %+v", groupKeys(groups))
	}
	if meta.Models != 2 || meta.Games != 104 || meta.Wins != 90 {
		t.Errorf("meta = %d models / %d games / %dW, want 2/104/90", meta.Models, meta.Games, meta.Wins)
	}
	// Pooled: 90/(90+10+0+4) = 0.8654. A mean of the two models' rates would be
	// (0.9 + 0.0)/2 = 0.45 — a completely different and much worse answer.
	if meta.WinRate < 0.865 || meta.WinRate > 0.866 {
		t.Errorf("meta win rate = %.4f, want ~0.8654 (pooled, not averaged)", meta.WinRate)
	}
	// Legal rate pooled over decisions: (1000+20)/(1000+40).
	if meta.LegalRate < 0.980 || meta.LegalRate > 0.982 {
		t.Errorf("meta legal rate = %.4f, want 1020/1040", meta.LegalRate)
	}

	// Openness pools the two Llamas together and leaves gpt-4o out.
	open := find(GroupOpenness, OpenWeights)
	if open == nil || open.Models != 2 || open.Tokens != 104_000 {
		t.Errorf("open-weights group = %+v, want the two llamas and 104k tokens", open)
	}
	prop := find(GroupOpenness, Proprietary)
	if prop == nil || prop.Models != 1 || prop.Tokens != 50_000 {
		t.Errorf("proprietary group = %+v, want gpt-4o only", prop)
	}

	// Hosting separates the self-hosted Llama from the two paid APIs.
	local := find(GroupHosting, SelfHosted)
	if local == nil || local.Models != 1 || local.Matches != 4 {
		t.Errorf("local group = %+v, want the ollama model only", local)
	}
	hosted := find(GroupHosting, HostedAPI)
	if hosted == nil || hosted.Models != 2 || hosted.Matches != 150 {
		t.Errorf("hosted group = %+v, want groq + openai", hosted)
	}
	// A self-hosted group with no per-token bill must not claim a cost.
	if local.CostPerWin != 0 || local.CostBasis != "" {
		t.Errorf("local costPerWin=%.4f basis=%q, want no cost claimed", local.CostPerWin, local.CostBasis)
	}

	// Provider groups credit who SERVED; vendor groups credit who BUILT. Groq must
	// appear as a provider and never as the vendor of Meta's model.
	if g := find(GroupProvider, "groq"); g == nil || g.Models != 1 {
		t.Errorf("groq provider group = %+v", g)
	}
	if g := find(GroupVendor, "groq"); g != nil {
		t.Errorf("groq must not be a VENDOR — it serves Meta's weights, it did not build them")
	}
}

// Every group's totals must reconcile against the rows the board actually showed.
func TestGroupTotalsReconcileWithModels(t *testing.T) {
	models := []ModelStat{
		{Provider: "openai", Model: "gpt-4o", Matches: 10, Wins: 6, Losses: 4, Tokens: 1000},
		{Provider: "anthropic", Model: "claude-sonnet-4", Matches: 20, Wins: 11, Losses: 9, Tokens: 2000},
		{Provider: "ollama", Model: "qwen2.5", Matches: 5, Wins: 2, Losses: 3, Tokens: 500},
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	groups := BuildGroups(models)

	// Summing one whole axis must equal the whole board, on every axis.
	for _, kind := range []string{GroupProvider, GroupVendor, GroupOpenness, GroupHosting, GroupFamily} {
		var matches, wins int
		var tokens int64
		for _, g := range groups {
			if g.Kind != kind {
				continue
			}
			matches += g.Matches
			wins += g.Wins
			tokens += g.Tokens
		}
		if matches != 35 || wins != 19 || tokens != 3500 {
			t.Errorf("axis %s sums to %d matches / %dW / %d tokens, want 35/19/3500",
				kind, matches, wins, tokens)
		}
	}
}

// A model nobody has ever seen must appear in the comparison immediately — that is the
// whole promise of deriving the taxonomy instead of configuring it.
func TestBrandNewModelJoinsGroupsImmediately(t *testing.T) {
	models := []ModelStat{
		{Provider: "openai", Model: "gpt-4o", Matches: 10, Wins: 5, Losses: 5},
	}
	for i := range models {
		deriveModelStat(&models[i])
	}
	before := len(BuildGroups(models))

	// A new agent shows up running something released this morning on its own hardware.
	newcomer := ModelStat{Provider: "ollama", Model: "brand-new-thing-2026", Matches: 3, Wins: 3}
	deriveModelStat(&newcomer)
	models = append(models, newcomer)
	after := BuildGroups(models)

	if len(after) <= before {
		t.Fatal("a new model produced no new comparison groups")
	}
	var sawIt bool
	for _, g := range after {
		if g.Kind == GroupProvider && g.Key == "ollama" && g.Matches == 3 {
			sawIt = true
		}
	}
	if !sawIt {
		t.Errorf("the new model did not join a provider group: %+v", groupKeys(after))
	}
	// And it lands in open-weights, because it is self-hosted.
	for _, g := range after {
		if g.Kind == GroupOpenness && g.Key == OpenWeights && g.Models == 1 {
			return
		}
	}
	t.Error("a self-hosted newcomer should join the open-weights comparison")
}

func groupKeys(gs []GroupStat) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Kind+":"+g.Key)
	}
	return out
}
