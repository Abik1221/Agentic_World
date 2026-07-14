package twofa

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/secretbox"
	"github.com/agent-arena/arena/internal/totp"
)

type fakeRepo struct {
	enc     []byte
	enabled bool
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
	r.enc, r.enabled = nil, false
	return nil
}

func TestEnrollStepUpAndDisable(t *testing.T) {
	cipher, err := secretbox.New("test-master-key-at-least-32-bytes-long!!")
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{}
	now := time.Unix(1_700_000_000, 0)
	svc := New(repo, cipher, platform.FixedClock{T: now}, "pyyol")
	ctx := context.Background()

	// Not enrolled ⇒ step-up is a no-op (money flows normally).
	if err := svc.Require(ctx, "usr_a", ""); err != nil {
		t.Fatalf("Require before enrollment = %v, want nil", err)
	}

	secret, uri, err := svc.Setup(ctx, "usr_a", "usr_a")
	if err != nil || secret == "" || uri == "" {
		t.Fatalf("Setup failed: secret=%q uri=%q err=%v", secret, uri, err)
	}
	// Pending (not confirmed) ⇒ still a no-op.
	if err := svc.Require(ctx, "usr_a", ""); err != nil {
		t.Fatalf("Require while pending = %v, want nil", err)
	}

	wrong, _ := totp.Code(secret, now.Add(10*time.Minute)) // outside the skew window
	if err := svc.Confirm(ctx, "usr_a", wrong); err != ErrInvalidCode {
		t.Fatalf("Confirm wrong code = %v, want ErrInvalidCode", err)
	}
	code, _ := totp.Code(secret, now)
	if err := svc.Confirm(ctx, "usr_a", code); err != nil {
		t.Fatalf("Confirm valid code: %v", err)
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
	if _, _, err := svc.Setup(ctx, "usr_a", "usr_a"); err != ErrAlreadyEnabled {
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
