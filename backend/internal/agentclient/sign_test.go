package agentclient

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacHex(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// TestSignRequest locks the canonical signing construction so the JS/TS + Python
// SDKs can reproduce it byte-for-byte. If this vector changes, every SDK's verify
// helper must change too (and SignatureVersion should bump).
func TestSignRequest(t *testing.T) {
	const (
		secret = "test-endpoint-secret"
		ts     = "2026-07-06T12:00:00Z"
		nonce  = "req_abc123"
		method = "POST"
		path   = "/turn"
	)
	body := []byte(`{"game":"goofspiel","round":1}`)

	got := SignRequest(secret, ts, nonce, method, path, body)

	// Recompute independently to guard against accidental construction changes.
	want := hmacHex(secret, ts+"\n"+nonce+"\nPOST\n"+path+"\n"+sha256Hex(body))
	if got != want {
		t.Fatalf("signature mismatch:\n got=%s\nwant=%s", got, want)
	}

	// Deterministic + sensitive to every field.
	if SignRequest(secret, ts, nonce, method, path, body) != got {
		t.Fatal("signature not deterministic")
	}
	if SignRequest(secret, ts, nonce, "GET", path, body) == got {
		t.Fatal("signature must depend on method")
	}
	if SignRequest(secret, ts, nonce, method, "/event", body) == got {
		t.Fatal("signature must depend on path")
	}
	if SignRequest(secret, ts, nonce, method, path, []byte(`{"x":1}`)) == got {
		t.Fatal("signature must depend on body")
	}
	if SignRequest("other-secret", ts, nonce, method, path, body) == got {
		t.Fatal("signature must depend on secret")
	}
}
