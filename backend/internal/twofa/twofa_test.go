package twofa

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/totp"
)

type fakeRepo struct {
	enc      []byte
	enabled  bool
	recovery []string
}

func (r *fakeRepo) SaveSecret(_ context.Context, _ string, e []byte) error {
	r.enc, r.enabled = e, false
	return nil
}
func (r *fakeRepo) Load(_ context.Context, _ string) ([]byte, bool, error) {
	return r.enc, r.enabled, nil
}
func (r *fakeRepo) SetEnabled(_ context.Context, _ string, _ time.Time) error {
	r.enabled = true
	return nil
}
func (r *fakeRepo) Clear(_ context.Context, _ string) error {
	r.enc, r.enabled, r.recovery = nil, false, nil
	return nil
}
func (r *fakeRepo) SetRecoveryHashes(_ context.Context, _ string, hashes []string) error {
	r.recovery = append([]string(nil), hashes...)
	return nil
}
func (r *fakeRepo) ConsumeRecoveryHash(_ context.Context, _, hash string) (bool, error) {
	for i, h := range r.recovery {
		if h == hash {
			r.recovery = append(r.recovery[:i], r.recovery[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeRepo) RecoveryRemaining(_ context.Context, _ string) (int, error) {
	return len(r.recovery), nil
}

func TestEnrollStepUpAndDisable(t *testing.T) {
	cipher, err := secretbox.New("test-master-key-at-least-32-bytes-long!!")
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{}
	now := time.Unix(1_700_000_000, 0)
	svc := New(repo, cipher, platform.FixedClock{T: now}, "Pyyol", "recovery-pepper-secret")
	ctx := context.Background()

	// Not enrolled ⇒ step-up is a no-op (money flows normally).
	if err := svc.Require(ctx, "usr_a", ""); err != nil {
		t.Fatalf("Require before enrollment = %v, want nil", err)
	}

	secret, uri, err := svc.Setup(ctx, "usr_a", "alice")
	if err != nil || secret == "" || uri == "" {
		t.Fatalf("Setup failed: secret=%q uri=%q err=%v", secret, uri, err)
	}
	if !strings.Contains(uri, "alice") || !strings.Contains(uri, "Pyyol") {
		t.Fatalf("otpauth URI should use username + Pyyol issuer, got %s", uri)
	}
	// Pending (not confirmed) ⇒ still a no-op.
	if err := svc.Require(ctx, "usr_a", ""); err != nil {
		t.Fatalf("Require while pending = %v, want nil", err)
	}

	wrong, _ := totp.Code(secret, now.Add(10*time.Minute)) // outside the skew window
	if _, err := svc.Confirm(ctx, "usr_a", wrong); err != ErrInvalidCode {
		t.Fatalf("Confirm wrong code = %v, want ErrInvalidCode", err)
	}
	code, _ := totp.Code(secret, now)
	codes, err := svc.Confirm(ctx, "usr_a", code)
	if err != nil {
		t.Fatalf("Confirm valid code: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), recoveryCodeCount)
	}

	// Enabled ⇒ step-up enforced.
	if err := svc.Require(ctx, "usr_a", ""); err != ErrCodeRequired {
		t.Fatalf("Require no code = %v, want ErrCodeRequired", err)
	}
	if err := svc.Require(ctx, "usr_a", wrong); err != ErrInvalidCode {
		t.Fatalf("Require wrong code = %v, want ErrInvalidCode", err)
	}
	if err := svc.Require(ctx, "usr_a", code); err != nil {
		t.Fatalf("Require valid code: %v", err)
	}

	// Re-setup while enabled is refused.
	if _, _, err := svc.Setup(ctx, "usr_a", "alice"); err != ErrAlreadyEnabled {
		t.Fatalf("Setup while enabled = %v, want ErrAlreadyEnabled", err)
	}

	// Disable needs a valid code; after it, step-up is a no-op again.
	if err := svc.Disable(ctx, "usr_a", wrong); err != ErrInvalidCode {
		t.Fatalf("Disable wrong code = %v, want ErrInvalidCode", err)
	}
	if err := svc.Disable(ctx, "usr_a", code); err != nil {
		t.Fatalf("Disable valid code: %v", err)
	}
	if err := svc.Require(ctx, "usr_a", ""); err != nil {
		t.Fatalf("Require after disable = %v, want nil", err)
	}
}

// A lost authenticator is recoverable: a one-time recovery code satisfies step-up,
// is consumed (can't be replayed), and the remaining count drops.
func TestRecoveryCodeStepUp(t *testing.T) {
	cipher, _ := secretbox.New("test-master-key-at-least-32-bytes-long!!")
	repo := &fakeRepo{}
	now := time.Unix(1_700_000_000, 0)
	svc := New(repo, cipher, platform.FixedClock{T: now}, "pyyol", "recovery-pepper-secret")
	ctx := context.Background()

	secret, _, _ := svc.Setup(ctx, "usr_a", "usr_a")
	code, _ := totp.Code(secret, now)
	recovery, err := svc.Confirm(ctx, "usr_a", code)
	if err != nil || len(recovery) == 0 {
		t.Fatalf("confirm/recovery setup failed: %v", err)
	}
	if n, _ := svc.RecoveryRemaining(ctx, "usr_a"); n != recoveryCodeCount {
		t.Fatalf("remaining = %d, want %d", n, recoveryCodeCount)
	}

	// Use a recovery code where a TOTP code would be expected (lost device).
	rc := recovery[0]
	if err := svc.Require(ctx, "usr_a", rc); err != nil {
		t.Fatalf("recovery code should satisfy step-up: %v", err)
	}
	// It is one-time: the same code no longer works, and the count dropped.
	if err := svc.Require(ctx, "usr_a", rc); err != ErrInvalidCode {
		t.Fatalf("reused recovery code = %v, want ErrInvalidCode", err)
	}
	if n, _ := svc.RecoveryRemaining(ctx, "usr_a"); n != recoveryCodeCount-1 {
		t.Fatalf("remaining after use = %d, want %d", n, recoveryCodeCount-1)
	}

	// Regenerate (gated by a live TOTP code) replaces the set.
	fresh, err := svc.RegenerateRecoveryCodes(ctx, "usr_a", code)
	if err != nil || len(fresh) != recoveryCodeCount {
		t.Fatalf("regenerate failed: codes=%d err=%v", len(fresh), err)
	}
	if svc.Require(ctx, "usr_a", rc) != ErrInvalidCode {
		t.Fatal("old recovery codes must be invalid after regeneration")
	}
}

func TestPickAccountLabel(t *testing.T) {
	cases := []struct {
		user, email, id, want string
	}{
		{"alice", "a@x.com", "usr_1", "alice"},
		{"@alice", "a@x.com", "usr_1", "alice"},
		{"", "a@x.com", "usr_1", "a@x.com"},
		{"", "", "usr_1", "usr_1"},
		{"  ", "  ", "usr_1", "usr_1"},
	}
	for _, c := range cases {
		if got := PickAccountLabel(c.user, c.email, c.id); got != c.want {
			t.Fatalf("PickAccountLabel(%q,%q,%q)=%q want %q", c.user, c.email, c.id, got, c.want)
		}
	}
}
