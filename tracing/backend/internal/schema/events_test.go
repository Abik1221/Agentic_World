package schema

import "testing"

func TestRedactMap_PreservesLegitimateCodeField(t *testing.T) {
	m := map[string]any{"code": "not_a_secret", "code_hash": "abc123"}
	out := redactMap(m)
	if out["code"] != "not_a_secret" {
		t.Fatalf("code: got %v", out["code"])
	}
}

func TestRedactPayloadByEventType_StripsCodeExecSource(t *testing.T) {
	m := map[string]any{"code": "SECRET_SOURCE"}
	out := redactPayloadByEventType("code_exec_started", m, 0)
	if out["code"] != "[redacted]" {
		t.Fatalf("code_exec code: got %v", out["code"])
	}
}

func TestRedactMap_AllowedDataPreviewKeepsTokenMention(t *testing.T) {
	m := map[string]any{
		"data_preview": `{"total_tokens":500,"note":"OAuth bearer flows"}`,
	}
	out := redactMap(m)
	s, ok := out["data_preview"].(string)
	if !ok || s == "[redacted]" {
		t.Fatalf("expected data_preview preserved, got %v", out["data_preview"])
	}
}

func TestRedactString_TokenCountNotRedacted(t *testing.T) {
	s := redactString("total_tokens usage is 500")
	if s != "total_tokens usage is 500" {
		t.Fatalf("got %q", s)
	}
}

func TestRedactString_AuthorizationBearerRedacted(t *testing.T) {
	s := redactString("upstream said: authorization: bearer eyJhbGc")
	if s != "[redacted]" {
		t.Fatalf("got %q", s)
	}
}

func TestRedactPayloadByEventType_NLP_Caps(t *testing.T) {
	long := make([]rune, 500)
	for i := range long {
		long[i] = 'a'
	}
	m := map[string]any{
		"input":  map[string]any{"text": string(long)},
		"output": map[string]any{"summary": string(long)},
	}
	out := redactPayloadByEventType("nlp_task_call", m, 240)
	in := out["input"].(map[string]any)
	if l := len([]rune(in["text"].(string))); l > 240+3 { // + ellipsis rune
		t.Fatalf("text len %d", l)
	}
}

func TestRedactPayloadByEventType_NLP_NoCapWhenZero(t *testing.T) {
	long := make([]rune, 500)
	for i := range long {
		long[i] = 'b'
	}
	text := string(long)
	m := map[string]any{
		"input": map[string]any{"text": text},
	}
	out := redactPayloadByEventType("nlp_task_call", m, 0)
	in := out["input"].(map[string]any)
	if in["text"].(string) != text {
		t.Fatalf("expected full text when cap is 0")
	}
}

func TestRedactPayloadByEventType_QuoteCap(t *testing.T) {
	m := map[string]any{
		"nested": map[string]any{
			"quote": string(makePad(300)),
		},
	}
	out := redactPayloadByEventType("citation_completed", m, 240)
	n := out["nested"].(map[string]any)
	if len([]rune(n["quote"].(string))) > 240+3 {
		t.Fatalf("quote not capped")
	}
}

func makePad(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}

func TestCanonicalEventType_Phase10(t *testing.T) {
	for _, et := range []string{
		"plan_emitted", "synthesis_started", "artifact_written", "subagent_spawned", "nlp_task_call",
	} {
		if _, ok := canonicalEventTypes[et]; !ok {
			t.Fatalf("missing %q", et)
		}
	}
}
