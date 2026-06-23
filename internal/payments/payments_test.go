package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/payments"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

const secret = "whsec_test"

var clockT = time.Unix(1_700_000_000, 0).UTC()

// ── fakes ────────────────────────────────────────────────────────────────────

// fakeCoiner models the ledger's idempotency: a repeated key is a no-op.
type fakeCoiner struct {
	seen       map[string]bool
	credited   int64
	reversed   int64
	topupCalls int
	reverseErr error
}

func newCoiner() *fakeCoiner { return &fakeCoiner{seen: map[string]bool{}} }

func (f *fakeCoiner) Topup(_ context.Context, _ string, coins int64, key string) error {
	f.topupCalls++
	if f.seen[key] {
		return nil
	}
	f.seen[key] = true
	f.credited += coins
	return nil
}
func (f *fakeCoiner) Reverse(_ context.Context, _ string, coins int64, key string) error {
	if f.reverseErr != nil {
		return f.reverseErr
	}
	if f.seen[key] {
		return nil
	}
	f.seen[key] = true
	f.reversed += coins
	return nil
}

type fakeRepo struct {
	events    map[string]payments.StoredEvent
	processed map[string]bool
	owner     string
	connectID string
}

func newRepo() *fakeRepo {
	return &fakeRepo{events: map[string]payments.StoredEvent{}, processed: map[string]bool{}, owner: "usr_a"}
}

func (r *fakeRepo) InsertEvent(_ context.Context, id, typ string, payload []byte) (bool, error) {
	if _, ok := r.events[id]; ok {
		return true, nil
	}
	r.events[id] = payments.StoredEvent{ID: id, Type: typ, Payload: payload}
	return false, nil
}
func (r *fakeRepo) MarkProcessed(_ context.Context, id string) error { r.processed[id] = true; return nil }
func (r *fakeRepo) UnprocessedEvents(_ context.Context, _ int) ([]payments.StoredEvent, error) {
	var out []payments.StoredEvent
	for id, e := range r.events {
		if !r.processed[id] {
			out = append(out, e)
		}
	}
	return out, nil
}
func (r *fakeRepo) OwnerOfAgent(_ context.Context, _ string) (string, error)  { return r.owner, nil }
func (r *fakeRepo) StripeConnectID(_ context.Context, _ string) (string, error) { return r.connectID, nil }
func (r *fakeRepo) SetStripeConnectID(_ context.Context, _, id string) error    { r.connectID = id; return nil }

func newSvc(coiner payments.Coiner, repo payments.Repo) *payments.Service {
	return payments.New(payments.DevGateway{}, coiner, repo,
		platform.FixedClock{T: clockT},
		payments.Config{
			Packs:         payments.DefaultPacks(),
			WebhookSecret: secret,
			SuccessURL:    "https://arena.test/billing/success",
			CancelURL:     "https://arena.test/billing/cancel",
		},
		slog.Default(), prometheus.NewRegistry())
}

func sign(payload []byte, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func eventJSON(id, typ, sessionID, agent string, coins int64) []byte {
	ev := map[string]any{
		"id":   id,
		"type": typ,
		"data": map[string]any{"object": map[string]any{
			"id":           sessionID,
			"amount_total": 500,
			"metadata":     map[string]string{"agent": agent, "user": "usr_a", "coins": strconv.FormatInt(coins, 10)},
		}},
	}
	b, _ := json.Marshal(ev)
	return b
}

func codeOf(err error) string {
	var ae *httpx.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

// ── signature ────────────────────────────────────────────────────────────────

func TestVerifySignature(t *testing.T) {
	payload := []byte(`{"id":"evt_1"}`)
	now := clockT
	tol := 5 * time.Minute

	if err := payments.VerifySignature(payload, sign(payload, now.Unix()), secret, now, tol); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := payments.VerifySignature([]byte(`{"id":"tampered"}`), sign(payload, now.Unix()), secret, now, tol); err != payments.ErrBadSignature {
		t.Fatalf("tampered payload = %v, want ErrBadSignature", err)
	}
	old := now.Add(-10 * time.Minute).Unix()
	if err := payments.VerifySignature(payload, sign(payload, old), secret, now, tol); err != payments.ErrSignatureExpired {
		t.Fatalf("stale timestamp = %v, want ErrSignatureExpired", err)
	}
}

// ── webhook ──────────────────────────────────────────────────────────────────

func TestWebhookCreditsExactlyOnceOnRedelivery(t *testing.T) {
	coiner := newCoiner()
	svc := newSvc(coiner, newRepo())
	body := eventJSON("evt_1", payments.EventCheckoutCompleted, "cs_1", "ag_a", 550)
	header := sign(body, clockT.Unix())

	for i := 0; i < 3; i++ { // Stripe delivers at-least-once
		if err := svc.HandleWebhook(context.Background(), body, header); err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
	}
	if coiner.credited != 550 {
		t.Fatalf("credited = %d, want 550 (exactly once)", coiner.credited)
	}
	if coiner.topupCalls < 1 {
		t.Fatal("expected the topup path to be exercised")
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	coiner := newCoiner()
	svc := newSvc(coiner, newRepo())
	body := eventJSON("evt_2", payments.EventCheckoutCompleted, "cs_2", "ag_a", 100)

	err := svc.HandleWebhook(context.Background(), body, "t="+strconv.FormatInt(clockT.Unix(), 10)+",v1=deadbeef")
	if err != payments.ErrBadSignature {
		t.Fatalf("bad signature = %v, want ErrBadSignature", err)
	}
	if coiner.credited != 0 {
		t.Fatal("nothing should be credited for an unverified webhook")
	}
}

func TestWebhookRefundReverses(t *testing.T) {
	coiner := newCoiner()
	svc := newSvc(coiner, newRepo())
	body := eventJSON("evt_3", payments.EventChargeRefunded, "ch_3", "ag_a", 100)
	if err := svc.HandleWebhook(context.Background(), body, sign(body, clockT.Unix())); err != nil {
		t.Fatalf("refund webhook: %v", err)
	}
	if coiner.reversed != 100 {
		t.Fatalf("reversed = %d, want 100", coiner.reversed)
	}
}

// ── topup ────────────────────────────────────────────────────────────────────

func TestTopupRejectsUnknownPack(t *testing.T) {
	svc := newSvc(newCoiner(), newRepo())
	if _, err := svc.Topup(context.Background(), "usr_a", "ag_a", "nope"); err != payments.ErrUnknownPack {
		t.Fatalf("unknown pack = %v, want ErrUnknownPack", err)
	}
}

func TestTopupRejectsNonOwner(t *testing.T) {
	repo := newRepo()
	repo.owner = "usr_someone_else"
	svc := newSvc(newCoiner(), repo)
	if _, err := svc.Topup(context.Background(), "usr_a", "ag_a", "plus"); err != payments.ErrForbiddenAgent {
		t.Fatalf("non-owner topup = %v, want ErrForbiddenAgent", err)
	}
}

func TestTopupHappyPathReturnsURL(t *testing.T) {
	svc := newSvc(newCoiner(), newRepo()) // repo.owner defaults to usr_a
	co, err := svc.Topup(context.Background(), "usr_a", "ag_a", "plus")
	if err != nil {
		t.Fatalf("Topup: %v", err)
	}
	if co.URL == "" || co.ID == "" {
		t.Fatalf("expected a checkout url+id, got %+v", co)
	}
	if codeOf(err) != "" {
		t.Fatal("unexpected api error")
	}
}
