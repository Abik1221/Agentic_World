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
	log.Printf("e2ebench listening on %s (season %d)", addr, svc.CurrentSeason())
	log.Fatal(http.ListenAndServe(addr, r))
}
