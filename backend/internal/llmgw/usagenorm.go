package llmgw

import (
	"encoding/json"
	"sort"
	"strings"
)

// Usage normalization, by MEANING rather than by vendor.
//
// # Why the vendor table had to go
//
// This decoded three named shapes: OpenAI, Anthropic, Google. Everything else recorded zero
// tokens and therefore zero cost — silently, because a zero is a plausible-looking integer.
// A DeepSeek agent's caching was invisible, a Cohere agent's spend was $0, and a self-hosted
// vLLM server reported nothing at all. On a platform that ranks on cost efficiency, "unknown
// provider" scoring zero cost is not a gap, it is a way to win.
//
// Enumerating providers cannot fix that. New ones ship constantly, every open-source server
// (vLLM, Ollama, llama.cpp, LM Studio, SGLang, TGI) has its own dialect, and the table is
// always one release behind. What is stable is not the field NAMES but the CONCEPTS: input,
// output, cache read, cache write, reasoning. Those are matched by key pattern here, so
// `prompt_cache_hit_tokens`, `cache_read_input_tokens`, `cached_tokens` and
// `cachedContentTokenCount` all land in one bucket without any of them being listed.
//
// # The subset/alongside problem, and the general rule
//
// Providers disagree about whether cached tokens are INCLUDED in the input count or reported
// beside it, and the disagreement is silent — both shapes are a plausible integer, so guessing
// wrong shows up only as a cost that is too low.
//
//	OpenAI      prompt_tokens INCLUDES prompt_tokens_details.cached_tokens
//	DeepSeek    prompt_tokens == prompt_cache_hit_tokens + prompt_cache_miss_tokens
//	Google      promptTokenCount INCLUDES cachedContentTokenCount
//	Anthropic   input_tokens EXCLUDES cache_read + cache_creation — they are added on top
//
// The rule is not a vendor list, it is the semantics of the word used. A key in the PROMPT
// family names the whole prompt, so cache is a subset. A key in the INPUT family names what was
// charged as fresh input, so cache is additive. That reading holds for Bedrock's inputTokens
// (Anthropic-derived, additive) as well as for every OpenAI-wire server.
//
// A reported TOTAL is used as a cross-check where one exists: if treating cache as additive
// would exceed the provider's own total, the subset reading is taken instead. So a future
// provider that uses "input" for a cache-inclusive total is corrected by its own arithmetic
// rather than being mispriced.

// usageConcept is what a numeric field means, independent of what it is called.
type usageConcept int

const (
	conceptNone        usageConcept = iota
	conceptInputPrompt              // whole-prompt family: cache is a SUBSET
	conceptInputFresh               // fresh-input family: cache is ADDITIVE
	conceptOutput
	conceptCacheRead
	conceptCacheWrite
	conceptReasoning
	conceptTotal
)

// classifyUsageKey maps a response field name onto the concept it carries.
//
// Ordered most-specific-first: "cache_read_input_tokens" contains both "cache" and "input", and
// must be read as a cache field rather than as an input count. Getting that order wrong would
// make Anthropic's cache read look like its input total.
func classifyUsageKey(key string) usageConcept {
	k := strings.ToLower(key)
	k = strings.ReplaceAll(k, "-", "_")

	// Anything cache-flavoured first, so cache keys cannot be captured by the input rules.
	if strings.Contains(k, "cach") {
		switch {
		// "creation" / "write" / "miss" describe tokens being PUT INTO the cache, billed above
		// the normal input rate (Anthropic charges 1.25x). A miss is not a cache write, but it
		// is the uncached remainder and must not be counted as a read — handled below.
		case strings.Contains(k, "creat"), strings.Contains(k, "writ"):
			return conceptCacheWrite
		case strings.Contains(k, "miss"):
			// Explicitly NOT a cache concept: a miss is ordinary uncached input, already
			// included in the prompt total that accompanies it. Counting it would double-bill.
			return conceptNone
		case strings.Contains(k, "read"), strings.Contains(k, "hit"), strings.Contains(k, "cached"):
			return conceptCacheRead
		}
		// A bare "cache" count with no direction: treat as a read, the cheaper and therefore
		// conservative reading — overstating a discount is worse than understating it.
		return conceptCacheRead
	}
	if strings.Contains(k, "reasoning") || strings.Contains(k, "thought") {
		return conceptReasoning
	}
	if strings.Contains(k, "total") {
		return conceptTotal
	}
	// Output before input: "output_tokens" and "completion_tokens" are unambiguous, and doing
	// them first keeps the input rules from having to exclude them.
	if strings.Contains(k, "completion") || strings.Contains(k, "output") ||
		strings.Contains(k, "candidates") || k == "eval_count" {
		return conceptOutput
	}
	// The PROMPT family: the word names the whole prompt, so cache is a subset of it.
	if strings.Contains(k, "prompt") {
		return conceptInputPrompt
	}
	// The INPUT family: names fresh input, so cache is reported beside it.
	if strings.Contains(k, "input") {
		return conceptInputFresh
	}
	return conceptNone
}

// harvestedUsage is what a walk of a response found, before reconciliation.
type harvestedUsage struct {
	inputPrompt int
	inputFresh  int
	output      int
	cacheRead   int
	cacheWrite  int
	reasoning   int
	total       int
	// found reports whether ANY usage-shaped number was seen. False is the signal worth acting
	// on: it means a provider shape nobody has met yet, and a call recorded with no usage is
	// a call that contributes zero cost to every board.
	found bool
	model string
}

// harvestUsage walks any decoded JSON and accumulates usage by concept.
//
// Takes the MAXIMUM per concept rather than the last value seen. Responses repeat counts (a
// streamed body carries them across frames, a nested envelope may restate them), and a later
// zero for a field a provider is not reporting in that frame would otherwise erase a real
// count already found.
func harvestUsage(node any, out *harvestedUsage) {
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			harvestUsage(item, out)
		}
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := n[k]
			// The resolved model, wherever it appears. The response's own value beats the
			// request's because it turns an alias like "-latest" into the concrete version
			// served, which is what a model leaderboard has to compare.
			if isModelKey(k) {
				if s, ok := v.(string); ok && s != "" && out.model == "" {
					out.model = s
				}
			}
			if f, ok := numericJSON(v); ok {
				if c := classifyUsageKey(k); c != conceptNone {
					assignMax(out, c, int(f))
				}
				continue
			}
			harvestUsage(v, out)
		}
	}
}

func assignMax(u *harvestedUsage, c usageConcept, v int) {
	if v <= 0 {
		return
	}
	set := func(dst *int) {
		if v > *dst {
			*dst = v
		}
	}
	switch c {
	case conceptInputPrompt:
		set(&u.inputPrompt)
	case conceptInputFresh:
		set(&u.inputFresh)
	case conceptOutput:
		set(&u.output)
	case conceptCacheRead:
		set(&u.cacheRead)
	case conceptCacheWrite:
		set(&u.cacheWrite)
	case conceptReasoning:
		set(&u.reasoning)
	case conceptTotal:
		set(&u.total)
	default:
		return
	}
	u.found = true
}

// isModelKey recognises the resolved-model field across naming styles.
//
// Normalizing away case and separators is what makes Gemini's camelCase `modelVersion` land
// alongside OpenAI's `model` — that difference alone cost the Google path its model label, so the
// board would have grouped every Gemini call under the request's alias instead of the version
// actually served.
//
// Matched against a closed set rather than by substring: a substring test would capture unrelated
// keys, and a wrong model label is worse than none because it silently merges two models' results.
func isModelKey(key string) bool {
	k := strings.ToLower(key)
	k = strings.NewReplacer("_", "", "-", "", ".", "").Replace(k)
	switch k {
	case "model", "modelversion", "modelid", "modelname", "modelslug":
		return true
	}
	return false
}

func numericJSON(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// reconcile turns harvested concepts into the one convention every consumer downstream expects:
// PromptTokens is the total billable input, with cache reads and writes as SUBSETS of it.
//
// Normalizing here rather than at each consumer is what keeps the observed-call table, the Lens
// span, the boards and the SDKs from each holding a different number for one call.
func (u harvestedUsage) reconcile() (prompt, completion, cacheRead, cacheWrite, reasoning int) {
	cacheRead, cacheWrite, reasoning = u.cacheRead, u.cacheWrite, u.reasoning
	completion = u.output

	switch {
	case u.inputPrompt > 0:
		// Whole-prompt family: cache is already inside the count.
		prompt = u.inputPrompt
	case u.inputFresh > 0:
		// Fresh-input family: cache is billed on top.
		prompt = u.inputFresh + cacheRead + cacheWrite
		// Cross-check against the provider's own total. If adding the cache overshoots it, the
		// provider was using "input" for a cache-inclusive total after all — believe its
		// arithmetic rather than our reading of its field name.
		if u.total > 0 && prompt > u.total-completion && u.total-completion >= u.inputFresh {
			prompt = u.total - completion
		}
	default:
		// No input count at all, but cache counts present: the cache IS the input we know about.
		prompt = cacheRead + cacheWrite
	}

	// A cache count larger than the prompt total cannot be a subset of it. Rather than let
	// pricing clamp the excess away as if it had never been billed, raise the total to cover it.
	if sum := cacheRead + cacheWrite; sum > prompt {
		prompt = sum
	}
	return prompt, completion, cacheRead, cacheWrite, reasoning
}

// applyUsageGeneric fills in a Call from any provider response, streamed or not.
//
// Returns false when the body carried no recognisable usage at all. The caller records that
// distinctly: an unreadable shape is the one thing here worth alerting on, because it means a
// real provider is being costed at zero and no test will ever notice.
func applyUsageGeneric(c *Call, body []byte) bool {
	var doc any
	if json.Unmarshal(body, &doc) != nil {
		// Not a single JSON object: an SSE stream, most likely. Reduce it to its frames and
		// harvest across them — a streamed response splits its counts, so no one frame has the
		// whole picture.
		if frames := sseJSONFrames(body); len(frames) > 0 {
			var u harvestedUsage
			for _, f := range frames {
				harvestPreferringEnvelope(f, &u)
			}
			return u.commit(c)
		}
		return false
	}
	var u harvestedUsage
	harvestPreferringEnvelope(doc, &u)
	return u.commit(c)
}

// usageContainerHints name a container that holds accounting, for the fallback pass.
var usageContainerHints = []string{"usage", "tokens", "accounting", "billing"}

// harvestPreferringEnvelope reads the CANONICAL usage envelope where one exists, and only scans
// more widely when there is none.
//
// # Why priority rather than "take the largest match"
//
// Once the walk is general, an unrelated number elsewhere in a response can be read as a token
// count. That is not hypothetical: a proxy echoing several dialects, or a provider that left a
// legacy field in place, produces a body carrying both a real `usage` object and a stray
// `prompt_eval_count` or `usageMetadata`. Keeping the largest value would let whichever number
// happened to be bigger decide what the developer is charged.
//
// `usage` is what OpenAI, Anthropic, Bedrock, Mistral and every OpenAI-wire server fill in. The
// alternatives are what a provider uses INSTEAD of it, never alongside — so it wins outright, and
// the three-tier order below is a statement about which envelope the provider actually filled in.
func harvestPreferringEnvelope(doc any, u *harvestedUsage) {
	// The model label is wanted regardless of which tier supplies the counts.
	harvestModel(doc, u, 0)

	// Tier 1: the canonical envelope, wherever it sits (Anthropic's streaming shape nests it
	// inside `message`, so this cannot be a top-level-only lookup).
	if envelopes := findByKey(doc, func(k string) bool { return normalizeKey(k) == "usage" }, 0); len(envelopes) > 0 {
		for _, e := range envelopes {
			harvestUsage(e, u)
		}
		if u.found {
			return
		}
	}
	// Tier 2: a differently-named accounting container (Google's usageMetadata, Cohere's
	// meta.tokens).
	if containers := findByKey(doc, isUsageContainerKey, 0); len(containers) > 0 {
		for _, ct := range containers {
			harvestUsage(ct, u)
		}
		if u.found {
			return
		}
	}
	// Tier 3: no envelope at all. Ollama natively puts its counts at the TOP LEVEL, and a local
	// model bills nothing — so a zero here is never contradicted by an invoice, which is exactly
	// why it went unnoticed. Scan everything rather than report it as free.
	harvestUsage(doc, u)
}

func normalizeKey(k string) string {
	r := strings.NewReplacer("_", "", "-", "", ".", "")
	return r.Replace(strings.ToLower(k))
}

func isUsageContainerKey(k string) bool {
	lk := strings.ToLower(k)
	for _, h := range usageContainerHints {
		if strings.Contains(lk, h) {
			return true
		}
	}
	return false
}

// findByKey collects every non-scalar value whose key satisfies match, in deterministic order.
func findByKey(node any, match func(string) bool, depth int) []any {
	if depth > 12 {
		return nil
	}
	var out []any
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			out = append(out, findByKey(item, match, depth+1)...)
		}
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := n[k]
			switch v.(type) {
			case map[string]any, []any:
				if match(k) {
					out = append(out, v)
					continue // do not also descend: the envelope is the unit
				}
				out = append(out, findByKey(v, match, depth+1)...)
			}
		}
	}
	return out
}

// harvestModel finds the resolved model label anywhere in the document.
func harvestModel(node any, u *harvestedUsage, depth int) {
	if depth > 12 || u.model != "" {
		return
	}
	switch n := node.(type) {
	case []any:
		for _, item := range n {
			harvestModel(item, u, depth+1)
		}
	case map[string]any:
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if isModelKey(k) {
				if s, ok := n[k].(string); ok && s != "" {
					u.model = s
					return
				}
			}
		}
		for _, k := range keys {
			harvestModel(n[k], u, depth+1)
		}
	}
}

func (u harvestedUsage) commit(c *Call) bool {
	if !u.found && u.model == "" {
		return false
	}
	if u.model != "" {
		c.Model = u.model
	}
	if !u.found {
		return false
	}
	c.PromptTokens, c.CompletionTokens, c.CachedReadTokens, c.CachedWriteTokens, c.ReasoningTokens =
		u.reconcile()
	return true
}

// sseJSONFrames decodes the `data:` payloads of an event stream.
//
// Tolerant by design: a frame that does not parse is skipped rather than failing the whole
// harvest, because a stream legitimately contains keep-alives, comments and a [DONE] sentinel.
func sseJSONFrames(body []byte) []any {
	var out []any
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var frame any
		if json.Unmarshal([]byte(payload), &frame) == nil {
			out = append(out, frame)
		}
	}
	return out
}
