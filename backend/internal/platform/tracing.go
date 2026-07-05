package platform

import "context"

// TracerProvider is the seam for distributed tracing. Stage 0 ships a no-op
// implementation so call sites and shutdown wiring already exist; the OpenTelemetry
// OTLP exporter is slotted in behind this interface in a later increment (when the
// first real spans are added) without touching any caller. This is a deliberate
// YAGNI choice — the abstraction is here, the heavy dependency is not, yet.
type TracerProvider interface {
	// Shutdown flushes and releases tracing resources. Safe to call once.
	Shutdown(ctx context.Context) error
}

type noopTracerProvider struct{}

func (noopTracerProvider) Shutdown(context.Context) error { return nil }

// NewTracerProvider returns a tracer provider. When endpoint is empty (default),
// a no-op provider is returned. The OTLP-backed provider will be enabled here.
func NewTracerProvider(_ context.Context, endpoint string) (TracerProvider, error) {
	_ = endpoint
	return noopTracerProvider{}, nil
}
