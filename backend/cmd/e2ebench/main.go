// Command e2ebench serves ONLY the public rating/benchmark read API against a real
// Postgres, so the web UI can be exercised end to end without booting the whole
// platform (wallets, payments, matchmaking, object storage…).
//
// Development harness for verifying the model board and model detail pages against
// real data. Not built into any image and not referenced by the server.
//
//	DATABASE_URL=postgres://… go run ./cmd/e2ebench
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/agent-arena/arena/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8099"
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	svc := rating.New(store.NewRatingRepo(pool), platform.FixedClock{T: time.Now()},
		rating.Config{SeasonLength: 30 * 24 * time.Hour}, prometheus.NewRegistry())
	r := chi.NewRouter()
	// Permissive CORS: this harness only ever serves a local browser.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			next.ServeHTTP(w, req)
		})
	})
	rating.NewHandler(svc, nil, false, nil).Register(r)

	// Bind first, then log the address the LISTENER reports rather than the raw ADDR
	// env string. Two reasons: it prints the real bound port (useful when ADDR is ":0"),
	// and the logged value comes from the net stack instead of the environment, so a
	// newline in ADDR cannot forge log lines (gosec G706).
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	// Explicit timeouts: a zero-value http.Server has none, so one stalled client can
	// hold a connection open forever (gosec G114). Generous, since this harness serves a
	// local browser doing large board reads.
	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	//nolint:gosec // G706: gosec taints ln through net.Listen(addr), but ln.Addr() is a
	// net.Addr built by the kernel from the parsed address — not the ADDR string — so it
	// cannot carry a newline. Logged deliberately: it reports the real bound port.
	log.Printf("e2ebench listening on %s (season %d)", ln.Addr(), svc.CurrentSeason())
	log.Fatal(srv.Serve(ln))
}
