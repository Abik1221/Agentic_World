package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// VerifySignature checks a Stripe-Signature header against the signing secret
// using Stripe's documented scheme: the header is "t=<unix>,v1=<hex>[,v1=...]"
// and the signed payload is "<t>.<raw body>" HMAC-SHA256'd with the secret. The
// timestamp must be within tolerance of now to blunt replay. This is the real
// algorithm (stdlib only) — identical for DevGateway and StripeGateway.
func VerifySignature(payload []byte, sigHeader, secret string, now time.Time, tolerance time.Duration) error {
	if secret == "" {
		return ErrNotConfigured
	}
	ts, sigs := parseSigHeader(sigHeader)
	if ts == 0 || len(sigs) == 0 {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrSignatureExpired
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := mac.Sum(nil)

	for _, s := range sigs {
		got, err := hex.DecodeString(s)
		if err != nil {
			continue
		}
		if hmac.Equal(got, expected) {
			return nil
		}
	}
	return ErrBadSignature
}

// parseSigHeader returns the t= timestamp and all v1= signatures from the header.
func parseSigHeader(h string) (int64, []string) {
	var ts int64
	var sigs []string
	for _, part := range strings.Split(h, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, _ = strconv.ParseInt(kv[1], 10, 64)
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	return ts, sigs
}
