# Purchase Pipeline Issue Analysis

## Problem Summary
**The purchase system NEVER confirms the buy in development/testing mode.** Users get redirected to success page but coins are never credited.

## Root Cause
The `DevGateway` (used when `STRIPE_SECRET_KEY` is not set) **does not simulate Stripe webhooks**. It only redirects to success URL without triggering coin credit.

### Payment Flow (Current Broken State)

```
1. Frontend: POST /v1/wallet/topup {pack, agent}
   ↓
2. Handler calls: paymentsSvc.Topup()
   ↓
3. Service calls: gateway.CreateCheckout(params)
   ↓
4. DevGateway returns: {ID: "cs_dev_xxx", URL: "success_page?session_id=..."}
   ↓
5. Frontend redirects to success_page
   ↓
6. ❌ NOTHING HAPPENS - No webhook triggered!
   ↓
7. Coins are NEVER credited because:
   - handleWebhook() never called
   - process() never runs
   - coiner.Topup() never executes
```

### What Should Happen

```
1. POST /v1/wallet/topup {pack, agent}
   ↓
2. Handler calls: paymentsSvc.Topup()
   ↓
3. Service calls: gateway.CreateCheckout(params)
   ↓
4. Gateway should either:
   A) Simulate webhook: Store metadata, trigger webhook processing
   B) Direct credit: Call coiner directly with idempotency key
   C) Callback URL: Return success URL that confirms purchase
   ↓
5. Coins ARE credited via coiner.Topup()
   ↓
6. Frontend receives coins in wallet
```

## Code Locations

### The Broken DevGateway
`internal/payments/dev_gateway.go:16-19` - Only returns URL, no webhook simulation:

```go
func (DevGateway) CreateCheckout(_ context.Context, p CheckoutParams) (Checkout, error) {
    id := platform.NewID("cs_dev")
    // BROKEN: No webhook, no coin credit, just redirect!
    return Checkout{ID: id, URL: p.SuccessURL + "?session_id=" + id}, nil
}
```

### Real Stripe Gateway (Works Correctly)
`internal/payments/stripe_gateway.go:34-59` - Creates real checkout with metadata:

```go
func (g *StripeGateway) CreateCheckout(ctx context.Context, p CheckoutParams) (Checkout, error) {
    // Sets metadata on BOTH session and payment_intent
    form.Set("metadata[user]", p.UserPublicID)
    form.Set("metadata[agent]", p.AgentPublicID)
    form.Set("metadata[coins]", strconv.FormatInt(p.Pack.Coins, 10))
    // Stripe later sends webhook with this metadata
    // Webhook triggers coin credit
}
```

### Webhook Processing (Never Called in Dev)
`internal/payments/service.go:101-123`:

```go
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, sigHeader string) error {
    ev, err := parseEvent(payload)        // Parse Stripe event
    alreadySeen, err := s.repo.InsertEvent(...) // Store event
    if err := s.process(ctx, ev); err != nil } // Process & credit coins
    return s.repo.MarkProcessed(...)
}
```

The `process()` method (lines 127-156) calls `coiner.Topup()` which actually credits the coins.
**This never happens in DevGateway.**

### Configuration
`cmd/server/main.go:201-206`:

```go
var gateway payments.Gateway = payments.DevGateway{}
if cfg.StripeSecretKey != "" {
    gateway = payments.NewStripeGateway(cfg.StripeSecretKey)
} else {
    log.Warn("STRIPE_SECRET_KEY unset: offline DevGateway (no real charges)")
}
```

## Solutions

### ✅ Solution 1: Make DevGateway Simulate Webhook (RECOMMENDED)
DevGateway needs access to the Service to call HandleWebhook after a short delay.

**Pros:**
- Tests the full webhook flow
- Identical to production behavior
- Tests idempotency deduplication

**Cons:**
- DevGateway needs dependency on Service (circular dependency risk)

### ✅ Solution 2: Add Dev Webhook Endpoint
Add `POST /v1/admin/payments/dev/confirm/{session_id}` endpoint that manually triggers webhook.

**Pros:**
- No circular dependency
- Simple, explicit, frontend-controlled
- Easy to debug

**Cons:**
- Extra endpoint only for dev
- Frontend must call it explicitly

### ✅ Solution 3: Modify Success URL with Auto-Confirm
Have DevGateway return a success URL that includes metadata, and add a handler that processes it.

**Pros:**
- No extra endpoint
- Happens automatically on redirect

**Cons:**
- Still adds special dev logic to handlers

### ✅ Solution 4: Direct Credit (Bypass Webhook)
DevGateway directly calls `coiner.Topup()` synchronously.

**Pros:**
- Simplest implementation
- Quick confirmation

**Cons:**
- Doesn't test webhook flow
- Doesn't test idempotency key deduplication

## Recommendation
**Implement Solution 1 + Solution 2 hybrid:**
- DevGateway tries to schedule webhook processing (if Service is available)
- Fallback: Add dev endpoint for manual confirmation if needed

This tests the full flow while staying testable.

## Impact
- **Severity:** CRITICAL - Purchase system doesn't work at all
- **Scope:** Dev/test environments only (prod uses real Stripe)
- **Users Affected:** Anyone testing purchase flow without Stripe key

## Testing Checklist
- [ ] DevGateway: POST /v1/wallet/topup returns session_id
- [ ] Webhook: Coins credited to agent wallet
- [ ] Idempotency: Calling same session twice credits only once
- [ ] History: /v1/wallet/history shows topup transaction
- [ ] Reconciliation: Payment reconciliation picks up unprocessed sessions
