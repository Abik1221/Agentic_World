package llmgw

import "testing"

// A sweep across provider families, including several the code has NO branch for.
//
// The point is that none of these are enumerated anywhere. They are normalized because
// classifyUsageKey reads what a field MEANS, so the test is really asking: does the general rule
// hold across the ecosystem, and does it still hold for the three shapes that used to be
// hardcoded?
//
// The assertions are exact token counts rather than "readable", because "we parsed something" is
// not the property that matters — a shape read with the wrong subset/alongside convention parses
// fine and misprices the call silently.
func TestUsageNormalizesAcrossProviderFamilies(t *testing.T) {
	cases := []struct {
		name string
		why  string
		body string
		// want* are the NORMALIZED values: prompt is total billable input with cache as a
		// subset of it, which is the one convention every consumer downstream expects.
		wantPrompt, wantCompletion, wantCacheRead, wantCacheWrite, wantReasoning int
		wantModel                                                                string
	}{
		{
			name: "openai_cache_is_a_subset",
			why:  "prompt_tokens ALREADY includes cached_tokens, so nothing may be added.",
			body: `{"model":"gpt-4o","usage":{"prompt_tokens":1200,"completion_tokens":80,
			        "total_tokens":1280,"prompt_tokens_details":{"cached_tokens":300},
			        "completion_tokens_details":{"reasoning_tokens":20}}}`,
			wantPrompt: 1200, wantCompletion: 80, wantCacheRead: 300, wantReasoning: 20,
			wantModel: "gpt-4o",
		},
		{
			name: "anthropic_cache_is_additive",
			why:  "input_tokens is only the UNCACHED remainder, so both cache counts are added.",
			body: `{"model":"claude-opus-4","usage":{"input_tokens":420,"output_tokens":90,
			        "cache_read_input_tokens":1500,"cache_creation_input_tokens":600}}`,
			wantPrompt: 2520, wantCompletion: 90, wantCacheRead: 1500, wantCacheWrite: 600,
			wantModel: "claude-opus-4",
		},
		{
			name: "deepseek_hit_is_a_subset_and_miss_is_not_a_cache_field",
			why: "DeepSeek documents prompt_tokens == hit + miss. Counting the MISS as cache " +
				"activity would double-bill it; adding the HIT would inflate the input total.",
			body: `{"model":"deepseek-chat","usage":{"prompt_tokens":1000,"completion_tokens":20,
			        "prompt_cache_hit_tokens":896,"prompt_cache_miss_tokens":104,"total_tokens":1020}}`,
			wantPrompt: 1000, wantCompletion: 20, wantCacheRead: 896,
			wantModel: "deepseek-chat",
		},
		{
			name: "google_gemini_nested_metadata",
			why:  "Different key names entirely, and the cache count is a subset of the prompt.",
			body: `{"modelVersion":"gemini-2.5-pro","usageMetadata":{"promptTokenCount":800,
			        "candidatesTokenCount":60,"cachedContentTokenCount":200,"thoughtsTokenCount":15}}`,
			wantPrompt: 800, wantCompletion: 60, wantCacheRead: 200, wantReasoning: 15,
			wantModel: "gemini-2.5-pro",
		},
		{
			name:       "cohere_nests_under_meta_tokens",
			why:        "No cache, and counts two levels deep. Previously recorded ZERO by the gateway.",
			body:       `{"meta":{"tokens":{"input_tokens":500,"output_tokens":40}}}`,
			wantPrompt: 500, wantCompletion: 40,
		},
		{
			name: "ollama_native_has_no_usage_object",
			why: "Counts sit at the TOP LEVEL with idiosyncratic names. A local model bills " +
				"nothing, so a zero here is never contradicted by an invoice — which is exactly " +
				"why it went unnoticed.",
			body:       `{"model":"llama3.3","prompt_eval_count":300,"eval_count":25,"done":true}`,
			wantPrompt: 300, wantCompletion: 25, wantModel: "llama3.3",
		},
		{
			name:       "bedrock_camelcase_input_output",
			why:        "camelCase names, fresh-input family, with a total for cross-checking.",
			body:       `{"usage":{"inputTokens":400,"outputTokens":30,"totalTokens":430}}`,
			wantPrompt: 400, wantCompletion: 30,
		},
		{
			name:       "mistral_openai_wire",
			why:        "OpenAI-wire, no cache. Covered with no Mistral branch.",
			body:       `{"model":"mistral-large-latest","usage":{"prompt_tokens":600,"completion_tokens":50,"total_tokens":650}}`,
			wantPrompt: 600, wantCompletion: 50, wantModel: "mistral-large-latest",
		},
		{
			name:       "vllm_selfhosted_openai_wire",
			why:        "A self-hosted server. Open-source deployments are what a vendor table can never track.",
			body:       `{"model":"Qwen3-32B","usage":{"prompt_tokens":900,"total_tokens":975,"completion_tokens":75}}`,
			wantPrompt: 900, wantCompletion: 75, wantModel: "Qwen3-32B",
		},
		{
			name: "unknown_future_provider_with_conventional_names",
			why: "An envelope nobody has shipped, using ordinary words. This is the whole claim: " +
				"a provider works on the day it appears, with no release.",
			body: `{"meta":{"model":"future-1","accounting":{"input_token_count":250,
			        "output_token_count":15,"cache_read_token_count":100}}}`,
			// Fresh-input family plus a cache read, so the cache is additive: 250 + 100.
			wantPrompt: 350, wantCompletion: 15, wantCacheRead: 100, wantModel: "future-1",
		},
		{
			name: "input_family_total_cross_check_prevents_overcounting",
			why: "A provider that uses 'input' for a cache-INCLUSIVE total would be overcounted " +
				"by the additive rule. Its own total_tokens contradicts that, and the arithmetic " +
				"wins over our reading of its field name.",
			body: `{"usage":{"input_tokens":1000,"output_tokens":50,"cached_tokens":400,"total_tokens":1050}}`,
			// Additive would give 1400; the total says input+output == 1050, so 1000 stands.
			wantPrompt: 1000, wantCompletion: 50, wantCacheRead: 400,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got Call
			if !applyUsageGeneric(&got, []byte(c.body)) {
				t.Fatalf("usage UNREADABLE, so this call would be costed at ZERO.\nwhy: %s", c.why)
			}
			check := func(field string, got, want int) {
				t.Helper()
				if got != want {
					t.Errorf("%s = %d, want %d\nwhy: %s", field, got, want, c.why)
				}
			}
			check("prompt", got.PromptTokens, c.wantPrompt)
			check("completion", got.CompletionTokens, c.wantCompletion)
			check("cache_read", got.CachedReadTokens, c.wantCacheRead)
			check("cache_write", got.CachedWriteTokens, c.wantCacheWrite)
			check("reasoning", got.ReasoningTokens, c.wantReasoning)
			if c.wantModel != "" && got.Model != c.wantModel {
				t.Errorf("model = %q, want %q", got.Model, c.wantModel)
			}
		})
	}
}

// A response with no usage at all must be reported as unreadable, not silently zeroed.
//
// This is the discoverability half. Without it a provider shape we cannot parse is
// indistinguishable from a free call, and the only symptom is a leaderboard that quietly
// favours whoever uses it.
func TestUnreadableUsageIsReportedRatherThanZeroed(t *testing.T) {
	for name, body := range map[string]string{
		"html error page": `<html><body>502 Bad Gateway</body></html>`,
		"empty object":    `{}`,
		"no numbers":      `{"id":"x","object":"chat.completion","choices":[{"message":{"content":"hi"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var c Call
			if applyUsageGeneric(&c, []byte(body)) {
				t.Fatalf("reported usage as readable for %q", body)
			}
		})
	}
}

// Streamed bodies harvest across frames, because no single frame carries the whole picture.
func TestStreamedUsageIsHarvestedAcrossFrames(t *testing.T) {
	sse := "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-4\"," +
		"\"usage\":{\"input_tokens\":400,\"cache_read_input_tokens\":1000,\"cache_creation_input_tokens\":200}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"…\"}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":90}}\n\n" +
		"data: [DONE]\n\n"
	var c Call
	if !applyUsageGeneric(&c, []byte(sse)) {
		t.Fatal("streamed usage unreadable")
	}
	// Anthropic's cache counts are additive: 400 + 1000 + 200.
	if c.PromptTokens != 1600 || c.CompletionTokens != 90 {
		t.Fatalf("prompt/completion = %d/%d, want 1600/90", c.PromptTokens, c.CompletionTokens)
	}
	if c.CachedReadTokens != 1000 || c.CachedWriteTokens != 200 {
		t.Fatalf("cache read/write = %d/%d, want 1000/200", c.CachedReadTokens, c.CachedWriteTokens)
	}
}

// A decoy must not set the cost of the call.
//
// Once the walk is general, an unrelated number elsewhere in the response can be read as a token
// count. A response carrying a real `usage` envelope AND a stray field from another dialect is the
// realistic case: a proxy that echoes several shapes, or a provider that left a legacy field in
// place. Keeping the LARGEST match would let whichever number happened to be bigger decide what
// the developer is charged — that is not a rule, it is a coin flip.
//
// The canonical `usage` envelope is what OpenAI, Anthropic, Bedrock, Mistral and every
// OpenAI-wire server fill in. Alternatives are used INSTEAD of it, never alongside, so it wins.
func TestACanonicalUsageEnvelopeOutranksDecoyFields(t *testing.T) {
	body := `{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":5},
	          "prompt_eval_count":9999,"usageMetadata":{"promptTokenCount":9999}}`
	var c Call
	if !applyUsageGeneric(&c, []byte(body)) {
		t.Fatal("usage unreadable")
	}
	if c.PromptTokens != 10 || c.CompletionTokens != 5 {
		t.Fatalf("prompt/completion = %d/%d, want 10/5 — a decoy field set the cost of the call",
			c.PromptTokens, c.CompletionTokens)
	}
}
