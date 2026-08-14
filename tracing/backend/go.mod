module github.com/agent-arena/pyyol-lens/backend

go 1.26.1

// Same standard-library advisory set as the arena backend; on the 1.26 line the fixes are
// in 1.26.6. Lens is a separate module on a separate Go minor, so it needs its own pin —
// bumping only the arena leaves this stack reported vulnerable by the same gate.
toolchain go1.26.6

require (
	github.com/ClickHouse/clickhouse-go/v2 v2.45.0
	github.com/gofiber/fiber/v2 v2.52.12
	github.com/google/uuid v1.6.0
	github.com/nats-io/nats.go v1.51.0
)

require (
	github.com/ClickHouse/ch-go v0.71.0 // indirect
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-faster/city v1.0.1 // indirect
	github.com/go-faster/errors v0.7.1 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/paulmach/orb v0.12.0 // indirect
	github.com/philhofer/fwd v1.1.3-0.20240916144458-20a13a1f6b7c // indirect
	github.com/pierrec/lz4/v4 v4.1.25 // indirect
	github.com/rivo/uniseg v0.2.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/tinylib/msgp v1.2.5 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasthttp v1.51.0 // indirect
	github.com/valyala/tcplisten v1.0.0 // indirect
	go.opentelemetry.io/otel v1.41.0 // indirect
	go.opentelemetry.io/otel/trace v1.41.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/crypto v0.49.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15 // indirect
)
