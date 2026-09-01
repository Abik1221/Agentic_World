// Command labcert runs one certification on the Pyyol Lab ladder and emits a signed,
// independently verifiable bundle.
//
// This is the operator's entry point to everything under internal/{gops,exploit,ladder,
// labdriver,labagent,attest}. Before it existed the whole pipeline was built and tested but
// unreachable — nothing called Advance, no AgentSource had a production implementation, and
// no spec was ever published. It was a instrument with no handle.
//
// # What a run does
//
//  1. publish the active spec if none exists (append-only; re-publishing is a no-op)
//  2. Phase A — the model plays a reference opponent across every board
//  3. fit its policy, solve a bootstrap mixture of best responses, pin the digest
//  4. Phase B — the mixture plays it live until the bound reaches target precision
//  5. write the certificate, then emit a signed bundle a stranger can recompute
//
// # Why a CLI and not a worker
//
// A run costs real inference money and takes real time, so the person spending it should be
// the one who starts it and watches it. A background worker that certified whatever appeared
// would be a way to spend an API budget by accident. When this becomes routine it belongs on
// a schedule; it does not begin there.
//
// # Usage
//
//	labcert -agent ag_x -provider openai -model gpt-5 \
//	        -base-url https://api.openai.com/v1 -api-key $KEY -out cert.json
//
// Point -base-url at the platform's own gateway to have the calls proxied, costed and
// completion-bound like any developer's. Pointed straight at a provider the run still works,
// but nothing about the result is verified inference — the certificate then attests to the
// measurement, not to which model produced it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/attest"
	"github.com/agent-arena/arena/internal/labagent"
	"github.com/agent-arena/arena/internal/labdriver"
	"github.com/agent-arena/arena/internal/ladder"
	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/agent-arena/arena/internal/store"
)

func main() {
	var (
		dsn      = flag.String("dsn", env("DATABASE_URL", ""), "Postgres DSN")
		agentID  = flag.String("agent", "", "agent public id to certify")
		provider = flag.String("provider", "", "provider name, recorded on the certificate")
		model    = flag.String("model", "", "model name, recorded on the certificate")
		baseURL  = flag.String("base-url", "", "OpenAI-compatible base URL (prefer the Pyyol gateway)")
		apiKey   = flag.String("api-key", env("LAB_API_KEY", ""), "bearer token for the endpoint")
		temp     = flag.Float64("temperature", 0, "sampling temperature; part of the experiment")
		out      = flag.String("out", "", "write the signed bundle here (default: stdout)")
		signSeed = flag.String("sign-seed", env("LAB_SIGN_SEED", ""), "base64 Ed25519 seed")
		dryRun   = flag.Bool("dry-run", false, "publish the spec and print the plan, play nothing")
		maxSteps = flag.Int("max-steps", 500, "safety stop on runner iterations")
		// Spec size overrides. Publishing a SMALLER ladder is a legitimate operator action,
		// not a debug hack: the shipped spec is ~3,600 matches and a validation run against a
		// rate-limited endpoint needs to be minutes, not hours. It publishes a NEW VERSION
		// rather than mutating v1, so a certificate always says which ladder produced it and
		// a small run can never be mistaken for a full one.
		specVer = flag.Int("spec-version", 0, "publish this spec version (0 = use active)")
		phaseA  = flag.Int("phase-a", 0, "override Phase A matches on the published spec")
		maxB    = flag.Int("max-phase-b", 0, "override the Phase B cap")
		boards  = flag.Int("boards", 0, "override how many prize orders to cycle")
	)
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(context.Background(), log, opts{
		dsn: *dsn, agentID: *agentID, provider: *provider, model: *model,
		baseURL: *baseURL, apiKey: *apiKey, temperature: *temp,
		out: *out, signSeed: *signSeed, dryRun: *dryRun, maxSteps: *maxSteps,
		specVer: *specVer, phaseA: *phaseA, maxB: *maxB, boards: *boards,
	}); err != nil {
		log.Error("labcert failed", "err", err)
		os.Exit(1)
	}
}

type opts struct {
	dsn, agentID, provider, model string
	baseURL, apiKey               string
	temperature                   float64
	out, signSeed                 string
	dryRun                        bool
	maxSteps                      int
	specVer, phaseA, maxB, boards int
}

func run(ctx context.Context, log *slog.Logger, o opts) error {
	switch {
	case o.dsn == "":
		return fmt.Errorf("-dsn or DATABASE_URL is required")
	case o.agentID == "":
		return fmt.Errorf("-agent is required")
	case !o.dryRun && o.model == "":
		// A certificate that does not say what it measured is not a certificate.
		return fmt.Errorf("-model is required; the certificate must record what was measured")
	}

	db, err := pgxpool.New(ctx, o.dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer db.Close()
	repo := store.NewLadderRepo(db)

	// Publish the shipped spec if the ladder has never been set up. Append-only and
	// idempotent: re-running this cannot alter a published version, and a run in flight
	// keeps the version it started on.
	// An explicit -spec-version publishes a sized variant and makes it active.
	if o.specVer > 0 {
		want := ladder.DefaultSpec()
		want.Version = o.specVer
		if o.phaseA > 0 {
			want.PhaseAMatches = o.phaseA
		}
		if o.maxB > 0 {
			want.MaxPhaseB = o.maxB
			if want.FirstCheckpoint > o.maxB {
				want.FirstCheckpoint = o.maxB
			}
		}
		if o.boards > 0 && o.boards <= len(want.PrizeOrders) {
			want.PrizeOrders = want.PrizeOrders[:o.boards]
		}
		if err := repo.PublishSpec(ctx, want, true); err != nil {
			return fmt.Errorf("publish spec v%d: %w", o.specVer, err)
		}
	}

	spec, err := repo.ActiveSpec(ctx)
	if err != nil {
		if err := repo.PublishSpec(ctx, ladder.DefaultSpec(), true); err != nil {
			return fmt.Errorf("publish default spec: %w", err)
		}
		if spec, err = repo.ActiveSpec(ctx); err != nil {
			return fmt.Errorf("read spec after publishing: %w", err)
		}
		log.Info("published the default ladder spec", "version", spec.Version)
	}
	hash, err := spec.Hash()
	if err != nil {
		return err
	}
	cost, err := attest.Spoof(spec)
	if err != nil {
		return err
	}
	log.Info("ladder spec",
		"version", spec.Version, "hash", hash[:12],
		"n", spec.N, "boards", len(spec.PrizeOrders),
		"phase_a", spec.PhaseAMatches, "max_phase_b", spec.MaxPhaseB,
		"prober_mixture", spec.ProberMixture, "target_precision", spec.TargetPrecision,
		"spoof_entries", cost.Entries)

	if o.dryRun {
		log.Info("dry run: nothing played",
			"would_play_up_to", spec.PhaseAMatches+spec.MaxPhaseB)
		return nil
	}

	src := labagent.NewLLMSource(labagent.Model{
		Provider: o.provider, Model: o.model,
		BaseURL: o.baseURL, APIKey: o.apiKey, Temperature: o.temperature,
	})
	svc := ladder.NewService(repo, labdriver.New(src))

	log.Info("certifying", "agent", o.agentID, "provider", o.provider, "model", o.model)
	started := time.Now()
	var last ladder.Step
	for i := 0; i < o.maxSteps; i++ {
		last, err = svc.Advance(ctx, o.agentID)
		if err != nil {
			return fmt.Errorf("step %d: %w", i, err)
		}
		log.Info("step", "action", last.Action.Kind, "matches", last.Action.Matches,
			"reason", last.Action.Reason,
			"fit_done", last.Run.FitDone, "certify_done", last.Run.CertifyDone)
		if last.Done {
			break
		}
	}
	if last.Certificate == nil {
		return fmt.Errorf("run did not finish within %d steps; it is resumable — re-run to "+
			"continue from where it stopped", o.maxSteps)
	}
	c := *last.Certificate
	log.Info("certificate",
		"lower", fmt.Sprintf("%.4f", c.LowerBound),
		"upper", fmt.Sprintf("%.4f", c.UpperBound),
		"separable", c.Separable, "games", c.Games, "fit", c.FitMatches,
		"kept", fmt.Sprintf("%.3f", c.KeptFraction),
		"stop", c.StopReason, "elapsed", time.Since(started).Round(time.Second))

	// The bundle carries the INPUTS, so a reader recomputes rather than trusts.
	counts, err := repo.FitCounts(ctx, c.RunID)
	if err != nil {
		return err
	}
	payoffs, err := repo.Payoffs(ctx, c.RunID)
	if err != nil {
		return err
	}
	prevHash, err := repo.ChainHead(ctx)
	if err != nil {
		return fmt.Errorf("read chain head: %w", err)
	}

	bundle := attest.Bundle{
		Version: attest.BundleVersion, Spec: spec, SpecHash: hash,
		FitCounts: counts, Payoffs: payoffs[:min(len(payoffs), c.Games)],
		Certificate: c, PrevHash: prevHash,
		IssuedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// Verify our OWN bundle before emitting it. If it does not reproduce here it will not
	// reproduce for a reader, and shipping it would be publishing a claim we cannot support.
	if err := bundle.Verify(); err != nil {
		return fmt.Errorf("refusing to emit a bundle that does not verify: %w", err)
	}
	// Link it only AFTER it verifies. Chaining a bundle we could not reproduce would put an
	// unverifiable link in the middle of the series and break every certificate after it.
	bundleHash, err := bundle.Hash()
	if err != nil {
		return err
	}
	if err := repo.AppendChain(ctx, c.RunID, prevHash, bundleHash); err != nil {
		return fmt.Errorf("append to the bundle chain: %w", err)
	}
	log.Info("chained", "prev", short12(prevHash), "hash", short12(bundleHash))

	var payload any = bundle
	if o.signSeed != "" {
		signer, err := platformsign.NewSigner(o.signSeed)
		if err != nil {
			return fmt.Errorf("signing key: %w", err)
		}
		pub, err := platformsign.PublicFromSeed(o.signSeed)
		if err != nil {
			return fmt.Errorf("derive public key: %w", err)
		}
		signed, err := attest.Sign(bundle, signer, pub)
		if err != nil {
			return err
		}
		payload = signed
	} else {
		log.Warn("no -sign-seed: emitting an UNSIGNED bundle. It is still recomputable, but " +
			"nobody can attribute it to you")
	}

	enc, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if o.out == "" {
		fmt.Println(string(enc))
		return nil
	}
	// 0600 rather than 0644. The bundle is meant to be published, but publishing it is a
	// deliberate act — copying it somewhere, not leaving it world-readable in whatever
	// directory the tool happened to run in. An unsigned bundle in particular is a document
	// nobody can attribute, so it should not be readable by every account on the host by
	// default.
	if err := os.WriteFile(o.out, enc, 0o600); err != nil {
		return err
	}
	log.Info("wrote bundle", "path", o.out, "bytes", len(enc))
	return nil
}

// short12 trims a hash for a log line.
func short12(h string) string {
	if h == "" {
		return "genesis"
	}
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
