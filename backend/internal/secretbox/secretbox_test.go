package secretbox

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	c, err := New("a-reasonably-long-operator-secret")
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("bearer-token-abc123")
	sealed, err := c.Seal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, msg) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := c.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round trip mismatch: %q != %q", got, msg)
	}
}

func TestSealIsNondeterministic(t *testing.T) {
	c, _ := New("secret")
	a, _ := c.Seal([]byte("x"))
	b, _ := c.Seal([]byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext must differ (random nonce)")
	}
}

func TestOpenRejectsTamper(t *testing.T) {
	c, _ := New("secret")
	sealed, _ := c.Seal([]byte("hello"))
	sealed[len(sealed)-1] ^= 0xFF // flip a tag bit
	if _, err := c.Open(sealed); err != ErrInvalidCiphertext {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	c1, _ := New("secret-one")
	c2, _ := New("secret-two")
	sealed, _ := c1.Seal([]byte("hello"))
	if _, err := c2.Open(sealed); err != ErrInvalidCiphertext {
		t.Fatalf("expected ErrInvalidCiphertext with wrong key, got %v", err)
	}
}

func TestOpenRejectsShort(t *testing.T) {
	c, _ := New("secret")
	if _, err := c.Open([]byte("tiny")); err != ErrInvalidCiphertext {
		t.Fatalf("expected ErrInvalidCiphertext, got %v", err)
	}
}

func TestNewRejectsEmpty(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected error for empty secret")
	}
}
