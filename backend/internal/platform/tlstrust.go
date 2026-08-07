package platform

import (
	"crypto/x509"
	"log/slog"
)

// CheckTLSTrust verifies the process can validate outbound TLS certificates.
//
// # Why this is checked at boot
//
// A container without a CA bundle looks completely healthy. It starts, serves, connects to
// Postgres and Redis over plain TCP, and passes every health check — while EVERY outbound HTTPS
// call fails with "certificate signed by unknown authority". For this platform that means it
// cannot verify a developer's agent endpoint (any real deployment is https), cannot reach an LLM
// provider, and cannot talk to any payment or chain service. None of it is visible from a probe.
//
// It was found the slow way: agent onboarding failed with "endpoint not verified", which reads
// as a fault in the developer's deployment, and the truth was that the server could not validate
// anyone's certificate. The lab image was debian-slim; production uses distroless/static, which
// ships the bundle. The divergence, not the base image, is the bug — a lab that cannot speak TLS
// cannot validate the one path it exists to validate.
//
// Logged at ERROR rather than fatal. A deployment that only serves internal HTTP traffic is
// degraded, not dead, and killing it on boot would turn a misconfiguration into an outage. The
// message names the consequence, because "no CA bundle" alone does not tell an operator that
// agent verification is about to fail for every user.
func CheckTLSTrust(log *slog.Logger) bool {
	if log == nil {
		log = slog.Default()
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		log.Error("NO TLS TRUST STORE: outbound HTTPS cannot be verified",
			"error", err,
			"impact", "agent endpoint verification, LLM provider calls and any https integration "+
				"will fail with 'certificate signed by unknown authority'",
			"fix", "use a base image that ships CA certificates (production uses "+
				"gcr.io/distroless/static-debian12) or install the ca-certificates package")
		return false
	}
	if n := len(pool.Subjects()); n == 0 { //nolint:staticcheck // Subjects() is deprecated but is the only count available
		log.Error("EMPTY TLS TRUST STORE: outbound HTTPS cannot be verified",
			"impact", "agent endpoint verification and every https integration will fail",
			"fix", "install ca-certificates in the runtime image")
		return false
	}
	return true
}
