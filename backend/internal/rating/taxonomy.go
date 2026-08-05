package rating

import "strings"

// Model taxonomy: turning a (provider, model) pair into the groups a reader actually
// wants to compare — "is OpenAI beating Anthropic here", "do open-weight models hold
// up against frontier ones", "which Llama generation is worth running".
//
// Classification is DERIVED, never configured. A model nobody has seen before is
// classified the moment its first match lands, so a new agent with a new model joins
// every comparison automatically rather than waiting for someone to add it to a list.
// Where a rule does not match, the group is "unknown" and says so — an unrecognised
// model is never quietly filed under a vendor it does not belong to.
//
// This lives server-side rather than in the SDK on purpose: it must produce the same
// answer for a gateway-verified row (where no SDK was involved at all) as for an
// SDK-observed one.

// Group kinds. Each is one axis of comparison the board can pivot on.
const (
	GroupProvider = "provider" // who served it: openai, anthropic, google, ollama…
	GroupVendor   = "vendor"   // who BUILT the model: openai, meta, mistral, deepseek…
	GroupOpenness = "openness" // open-weights vs proprietary
	GroupHosting  = "hosting"  // hosted API vs self-hosted/local
	GroupFamily   = "family"   // gpt-4o, claude-sonnet, llama-3, qwen…
)

// Openness values.
const (
	OpenWeights     = "open-weights"
	Proprietary     = "proprietary"
	OpennessUnknown = "unknown"
)

// Hosting values. Local matters for cost comparisons: a self-hosted open-weight model
// has no per-token bill, so ranking it next to a hosted one on cost without saying so
// would be meaningless.
const (
	HostedAPI      = "hosted"
	SelfHosted     = "local"
	HostingUnknown = "unknown"
)

// ModelClass is one model's position on every comparison axis.
type ModelClass struct {
	// Provider is who SERVED the call (normalized). May differ from Vendor: Groq
	// serving Llama is provider=groq, vendor=meta.
	Provider string `json:"provider"`
	// Vendor is who BUILT the model. This is the axis "is OpenAI better than
	// Anthropic" actually asks about, and reading it off the serving provider would
	// credit Groq for Meta's model.
	Vendor   string `json:"vendor"`
	Family   string `json:"family"`
	Openness string `json:"openness"`
	Hosting  string `json:"hosting"`
}

// providerInfo describes a serving provider.
type providerInfo struct {
	canonical string
	hosting   string
}

// Known serving providers, by the normalized key the SDK and gateway report. Anything
// absent is classified from the model string alone with hosting unknown.
var providerTable = map[string]providerInfo{
	"openai":       {"openai", HostedAPI},
	"azure":        {"azure-openai", HostedAPI},
	"azure-openai": {"azure-openai", HostedAPI},
	"anthropic":    {"anthropic", HostedAPI},
	"bedrock":      {"bedrock", HostedAPI},
	"google":       {"google", HostedAPI},
	"vertex":       {"google-vertex", HostedAPI},
	"groq":         {"groq", HostedAPI},
	"openrouter":   {"openrouter", HostedAPI},
	"together":     {"together", HostedAPI},
	"fireworks":    {"fireworks", HostedAPI},
	"deepinfra":    {"deepinfra", HostedAPI},
	"mistral":      {"mistral", HostedAPI},
	"deepseek":     {"deepseek", HostedAPI},
	"cohere":       {"cohere", HostedAPI},
	"xai":          {"xai", HostedAPI},
	"perplexity":   {"perplexity", HostedAPI},
	"cerebras":     {"cerebras", HostedAPI},
	"sambanova":    {"sambanova", HostedAPI},
	"nebius":       {"nebius", HostedAPI},
	"hyperbolic":   {"hyperbolic", HostedAPI},

	// Self-hosted runtimes. Free per token, and the whole point of tracking them
	// separately: an agent running Llama on its own GPU is a genuinely different
	// economic proposition from one paying Groq to serve the same weights.
	"ollama":      {"ollama", SelfHosted},
	"vllm":        {"vllm", SelfHosted},
	"lmstudio":    {"lm-studio", SelfHosted},
	"llamacpp":    {"llama.cpp", SelfHosted},
	"llama-cpp":   {"llama.cpp", SelfHosted},
	"tgi":         {"tgi", SelfHosted},
	"localai":     {"localai", SelfHosted},
	"self-hosted": {"self-hosted", SelfHosted},
	"local":       {"self-hosted", SelfHosted},
}

// familyRule maps a model-name substring to its family, vendor and openness. Ordered:
// the FIRST match wins, so more specific needles must precede more general ones
// ("gpt-4o-mini" before "gpt-4o", "llama-4" before "llama").
type familyRule struct {
	needle   string
	family   string
	vendor   string
	openness string
}

var familyRules = []familyRule{
	// ── OpenAI ───────────────────────────────────────────────────────────────
	{"gpt-4o-mini", "gpt-4o-mini", "openai", Proprietary},
	{"gpt-4o", "gpt-4o", "openai", Proprietary},
	{"gpt-4.1", "gpt-4.1", "openai", Proprietary},
	{"gpt-4", "gpt-4", "openai", Proprietary},
	{"gpt-5", "gpt-5", "openai", Proprietary},
	{"gpt-3.5", "gpt-3.5", "openai", Proprietary},
	{"o4-mini", "o4-mini", "openai", Proprietary},
	{"o3-mini", "o3-mini", "openai", Proprietary},
	{"o3", "o3", "openai", Proprietary},
	{"o1-mini", "o1-mini", "openai", Proprietary},
	{"o1", "o1", "openai", Proprietary},
	// OpenAI's open-weight release — same vendor, different openness, which is
	// exactly the distinction a "vendor" grouping alone would lose.
	{"gpt-oss", "gpt-oss", "openai", OpenWeights},

	// ── Anthropic ────────────────────────────────────────────────────────────
	{"opus", "claude-opus", "anthropic", Proprietary},
	{"sonnet", "claude-sonnet", "anthropic", Proprietary},
	{"haiku", "claude-haiku", "anthropic", Proprietary},
	{"claude", "claude", "anthropic", Proprietary},

	// ── Google ───────────────────────────────────────────────────────────────
	{"gemini-flash", "gemini-flash", "google", Proprietary},
	{"gemini", "gemini", "google", Proprietary},
	{"gemma", "gemma", "google", OpenWeights},

	// ── Meta ─────────────────────────────────────────────────────────────────
	{"llama-4", "llama-4", "meta", OpenWeights},
	{"llama4", "llama-4", "meta", OpenWeights},
	{"llama-3", "llama-3", "meta", OpenWeights},
	{"llama3", "llama-3", "meta", OpenWeights},
	{"llama", "llama", "meta", OpenWeights},

	// ── other open-weight families ───────────────────────────────────────────
	{"mixtral", "mixtral", "mistral", OpenWeights},
	{"ministral", "ministral", "mistral", OpenWeights},
	{"mistral", "mistral", "mistral", OpenWeights},
	{"qwen", "qwen", "alibaba", OpenWeights},
	{"deepseek", "deepseek", "deepseek", OpenWeights},
	{"phi-", "phi", "microsoft", OpenWeights},
	{"command-r", "command-r", "cohere", OpenWeights},
	{"command", "command", "cohere", Proprietary},
	{"grok", "grok", "xai", Proprietary},
	{"kimi", "kimi", "moonshot", OpenWeights},
	{"glm-", "glm", "zhipu", OpenWeights},
	{"yi-", "yi", "01ai", OpenWeights},
	{"nemotron", "nemotron", "nvidia", OpenWeights},
	{"olmo", "olmo", "allenai", OpenWeights},
	{"falcon", "falcon", "tii", OpenWeights},
	{"solar", "solar", "upstage", OpenWeights},
	{"granite", "granite", "ibm", OpenWeights},
	{"smollm", "smollm", "huggingface", OpenWeights},
	{"starcoder", "starcoder", "bigcode", OpenWeights},
}

// Classify places a (provider, model) pair on every comparison axis.
//
// The model string is matched after stripping the routing prefixes hosted providers
// bolt on — "openrouter/meta-llama/llama-3.3-70b-instruct" and
// "us.anthropic.claude-opus-4-1" name the same models as their bare forms, and
// treating them as different would split one family across several rows.
func Classify(provider, model string) ModelClass {
	p := strings.ToLower(strings.TrimSpace(provider))
	c := ModelClass{
		Provider: p,
		Vendor:   "unknown",
		Family:   "unknown",
		Openness: OpennessUnknown,
		Hosting:  HostingUnknown,
	}
	if p == "" {
		c.Provider = "unknown"
	}
	if info, ok := providerTable[p]; ok {
		c.Provider = info.canonical
		c.Hosting = info.hosting
	}

	m := normalizeModel(model)
	for _, r := range familyRules {
		if strings.Contains(m, r.needle) {
			c.Family, c.Vendor, c.Openness = r.family, r.vendor, r.openness
			break
		}
	}

	// A self-hosted model is open-weight by construction — you cannot run weights you
	// were never given. This is the one inference safe to make from hosting alone, and
	// it is what lets a local Ollama model nobody has a rule for still land in the
	// open-weights comparison instead of "unknown".
	if c.Hosting == SelfHosted && c.Openness == OpennessUnknown {
		c.Openness = OpenWeights
	}
	return c
}

// normalizeModel lowercases and strips provider routing prefixes and deployment
// decorations so the same underlying model matches one rule regardless of how it was
// addressed.
func normalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	// Ollama tags: "llama3.3:70b-instruct-q4_K_M" → drop the tag.
	if i := strings.IndexByte(m, ':'); i > 0 {
		m = m[:i]
	}
	// Routing prefixes: "openrouter/meta-llama/llama-3.3-70b" → "llama-3.3-70b".
	if i := strings.LastIndexByte(m, '/'); i >= 0 && i+1 < len(m) {
		m = m[i+1:]
	}
	// Bedrock region prefixes: "us.anthropic.claude-opus-4-1" → "claude-opus-4-1".
	// Only the LAST dotted segment is dropped-to, and only when the prefix looks like
	// a routing namespace rather than part of a version ("gpt-4.1" must survive).
	for _, prefix := range []string{"us.", "eu.", "apac.", "anthropic.", "meta.", "amazon.", "mistral.", "cohere."} {
		m = strings.TrimPrefix(m, prefix)
	}
	return m
}
