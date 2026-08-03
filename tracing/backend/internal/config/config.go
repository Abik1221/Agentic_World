package config

import (
	"os"
	"strings"
)

type Config struct {
	ServiceName string
	Environment string

	IngestPort  string
	QueryPort   string
	ControlPort string

	CHAddr   string
	CHDB     string
	CHUser   string
	CHPass   string
	CHSecure bool

	PGAddr     string
	PGUser     string
	PGPass     string
	PGDatabase string

	NATSURL           string
	NATSStreamName    string
	NATSSubjectEvents string
	NATSConsumerName  string

	ObjectStoreEndpoint string
	ObjectStoreBucket   string
	ObjectStoreKey      string
	ObjectStoreSecret   string
	ObjectStoreSSL      bool

	IngestAPIKey string
	// AdminAPIKey secures control-api replay routes; if empty, replay POSTs are rejected (set in production).
	AdminAPIKey string
	// QueryAPIKey secures the query-api read plane. The query-api trusts the
	// x-organization-id header verbatim (no per-caller identity), so without a
	// shared secret any network-reachable client could read ANY org's telemetry.
	// Empty in dev = open (localhost only); in production the query-api refuses to
	// boot when empty. Trusted callers (admin lens client, web proxy) send it as
	// the X-Pyyol-Key header.
	QueryAPIKey string

	// TrustedProxyCount is how many reverse proxies front these services, i.e. how many
	// X-Forwarded-For entries may be believed, counted from the RIGHT. 1 is correct for
	// the nginx vhost deployment. Raise only for a real extra hop; setting it too HIGH
	// is the dangerous direction, because it starts trusting client-supplied entries.
	TrustedProxyCount int
	// RateLimitPerMinute caps requests per caller per minute on the ingest and query
	// planes. Backstop behind the shared key: it bounds how fast an unauthenticated
	// caller can guess the key, and how much a leaked key can do before anyone notices.
	// 0 disables.
	RateLimitPerMinute int

	DefaultProject   string
	RetentionDays    int
	MaxBatchSize     int
	MaxEventsPerRead int
	// TelemetryTextCapRunes caps nested "quote" / nlp_task_call text in ingest (0 = unlimited).
	TelemetryTextCapRunes int
}

func Load() Config {
	return Config{
		ServiceName: get("SERVICE_NAME", "pyyol-lens"),
		Environment: get("PYYOL_LENS_ENV", "development"),

		IngestPort:  get("PORT_INGEST", "8081"),
		QueryPort:   get("PORT_QUERY", "8082"),
		ControlPort: get("PORT_CONTROL", "8083"),

		QueryAPIKey: os.Getenv("QUERY_API_KEY"),

		CHAddr:   get("CH_ADDR", "localhost:9000"),
		CHDB:     get("CH_DB", "pyyol_lens"),
		CHUser:   get("CH_USER", "default"),
		CHPass:   os.Getenv("CH_PASS"),
		CHSecure: get("CH_SECURE", "false") == "true",

		PGAddr:     get("PG_ADDR", "localhost:5432"),
		PGUser:     get("PG_USER", "pyyol_lens"),
		PGPass:     get("PG_PASS", "pyyol_lens"),
		PGDatabase: get("PG_DB", "pyyol_lens"),

		NATSURL:           get("NATS_URL", "nats://localhost:4222"),
		NATSStreamName:    get("NATS_STREAM_NAME", "PL_EVENTS"),
		NATSSubjectEvents: get("NATS_SUBJECT_EVENTS", "pyyol.lens.events"),
		NATSConsumerName:  get("NATS_CONSUMER_NAME", "pl-processor"),

		ObjectStoreEndpoint: get("OBJECT_STORE_ENDPOINT", "localhost:9001"),
		ObjectStoreBucket:   get("OBJECT_STORE_BUCKET", "pyyol-lens"),
		ObjectStoreKey:      get("OBJECT_STORE_KEY", "minioadmin"),
		ObjectStoreSecret:   get("OBJECT_STORE_SECRET", "minioadmin"),
		ObjectStoreSSL:      get("OBJECT_STORE_SSL", "false") == "true",

		IngestAPIKey: get("INGEST_API_KEY", "local-pyyol-lens-key"),
		AdminAPIKey:  get("PYYOL_LENS_ADMIN_KEY", ""),

		DefaultProject:   get("DEFAULT_PROJECT", "pyyol-core"),
		RetentionDays:    getInt("RETENTION_DAYS", 30),
		MaxBatchSize:     getInt("MAX_BATCH_SIZE", 500),
		MaxEventsPerRead: getInt("MAX_EVENTS_PER_READ", 500),

		TrustedProxyCount: getInt("TRUSTED_PROXY_COUNT", 1),
		// 600/min ≈ 10/s per caller. Generous for the arena batching spans and for a
		// human clicking round the trace UI, while still bounding a leaked key and making
		// key-guessing hopeless. Raise if a legitimate producer is throttled.
		RateLimitPerMinute: getInt("RATE_LIMIT_PER_MINUTE", 600),

		TelemetryTextCapRunes: telemetryTextCapRunes(),
	}
}

// telemetryTextCapRunes reads PYYOL_LENS_TELEMETRY_TEXT_CAP_RUNES: unset → 240, "0" → unlimited.
func telemetryTextCapRunes() int {
	v := strings.TrimSpace(os.Getenv("PYYOL_LENS_TELEMETRY_TEXT_CAP_RUNES"))
	if v == "" {
		return 240
	}
	if v == "0" {
		return 0
	}
	var n int
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 240
		}
		n = n*10 + int(ch-'0')
		if n > 1<<20 {
			return 1 << 20
		}
	}
	if n > 0 {
		return n
	}
	return 240
}

func get(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func getInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var out int
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				return d
			}
			out = out*10 + int(ch-'0')
		}
		if out > 0 {
			return out
		}
	}
	return d
}
