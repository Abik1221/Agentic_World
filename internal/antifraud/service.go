package antifraud

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// Config tunes detection.
type Config struct {
	CollusionMinGames int
	DetectLookback    time.Duration
}

// Service is the anti-fraud core: the payout gate, the disputes workflow, and the
// periodic detection job. It records holds/flags and resolves them via the Settler
// (wallet), auditing every action.
type Service struct {
	repo    Repo
	settler Settler
	clock   platform.Clock
	cfg     Config
	log     *slog.Logger
	m       *metrics
}

func New(repo Repo, settler Settler, clock platform.Clock, cfg Config, log *slog.Logger, reg *prometheus.Registry) *Service {
	if cfg.CollusionMinGames <= 0 {
		cfg.CollusionMinGames = minPairGames
	}
	if cfg.DetectLookback <= 0 {
		cfg.DetectLookback = 7 * 24 * time.Hour
	}
	return &Service{repo: repo, settler: settler, clock: clock, cfg: cfg, log: log, m: newMetrics(reg)}
}

// Allow implements wallet.PayoutGate. It holds a payout when the two seats share
// an owner (dumping bypass) or either agent is already flagged. On an internal
// error it fails CLOSED (holds), keeping money in escrow for review rather than
// risking a fraudulent payout.
func (s *Service) Allow(ctx context.Context, matchPublicID string) (bool, error) {
	agents, err := s.repo.MatchAgents(ctx, matchPublicID)
	if err != nil {
		s.log.Error("antifraud: gate lookup failed; holding payout", "match", matchPublicID, "error", err)
		_, _ = s.repo.RecordHold(ctx, matchPublicID, "gate_error")
		return false, nil
	}

	ids := make([]string, 0, len(agents))
	owners := map[string]bool{}
	sameOwner := false
	for _, a := range agents {
		ids = append(ids, a.AgentPublicID)
		if a.OwnerPublicID != "" && owners[a.OwnerPublicID] {
			sameOwner = true
		}
		owners[a.OwnerPublicID] = true
	}

	if sameOwner {
		for _, a := range agents {
			_ = s.repo.RecordFlag(ctx, a.AgentPublicID, matchPublicID, "same_owner", "both seats share an owner")
		}
		return s.hold(ctx, matchPublicID, "same_owner"), nil
	}

	flagged, err := s.repo.AnyFlagged(ctx, ids)
	if err != nil {
		s.log.Error("antifraud: flag lookup failed; holding payout", "match", matchPublicID, "error", err)
		return false, nil
	}
	if flagged {
		return s.hold(ctx, matchPublicID, "flagged_agent"), nil
	}
	return true, nil
}

// hold records a payout hold + flag metric and audits it. Returns false (the gate
// value: payout NOT allowed).
func (s *Service) hold(ctx context.Context, matchPublicID, reason string) bool {
	if newly, err := s.repo.RecordHold(ctx, matchPublicID, reason); err == nil && newly {
		s.m.holds.Inc()
		s.m.flags.WithLabelValues(reason).Inc()
		s.audit(ctx, "system", "payout_hold", matchPublicID, map[string]any{"reason": reason})
	}
	return false
}

// ReportDispute files a dispute and returns its public id.
func (s *Service) ReportDispute(ctx context.Context, reporterUserID, matchPublicID, agentPublicID, kind, detail string) (string, error) {
	id := platform.NewID("dsp")
	pub, err := s.repo.OpenDispute(ctx, DisputeInput{
		PublicID: id, MatchPublicID: matchPublicID, AgentPublicID: agentPublicID,
		ReporterUserID: reporterUserID, Kind: kind, Detail: detail,
	})
	if err != nil {
		return "", err
	}
	s.audit(ctx, reporterUserID, "dispute_open", pub, map[string]any{"kind": kind, "match": matchPublicID})
	return pub, nil
}

// ResolveDispute applies an admin action (refund | release | reject), idempotently.
// refund returns stakes; release pays out the held match; reject closes it.
func (s *Service) ResolveDispute(ctx context.Context, adminUserID, disputePublicID, action string) error {
	status, resolution := "resolved", action
	if action == "reject" {
		status = "rejected"
	}
	matchPublicID, changed, err := s.repo.ResolveDispute(ctx, disputePublicID, status, resolution)
	if err != nil {
		return err
	}
	if !changed {
		return nil // already terminal — idempotent no-op
	}

	switch action {
	case "refund":
		if matchPublicID != "" {
			if err := s.settler.Refund(ctx, matchPublicID); err != nil {
				return err
			}
			_ = s.repo.ResolveHold(ctx, matchPublicID, "refunded")
		}
	case "release":
		if matchPublicID != "" {
			if err := s.settler.SettleHeld(ctx, matchPublicID); err != nil {
				return err
			}
			_ = s.repo.ResolveHold(ctx, matchPublicID, "released")
		}
	}
	s.audit(ctx, adminUserID, "dispute_resolve:"+action, disputePublicID, map[string]any{"match": matchPublicID})
	return nil
}

// AgentTiming exposes an agent's timing profile (admin review).
func (s *Service) AgentTiming(ctx context.Context, agentPublicID string) (TimingStat, float64, bool, error) {
	t, err := s.repo.AgentTiming(ctx, agentPublicID)
	if err != nil {
		return TimingStat{}, 0, false, err
	}
	return t, HumanLikelihood(t), LooksHuman(t), nil
}

// RunDetection is the periodic sweep: flag collusion rings and human-like agents.
func (s *Service) RunDetection(ctx context.Context) error {
	pairs, err := s.repo.RecentPairs(ctx, s.clock.Now().Add(-s.cfg.DetectLookback), s.cfg.CollusionMinGames)
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if IsColluding(p) {
			_ = s.repo.RecordFlag(ctx, p.A, "", "collusion", "lopsided series + coin concentration")
			_ = s.repo.RecordFlag(ctx, p.B, "", "collusion", "lopsided series + coin concentration")
			s.m.flags.WithLabelValues("collusion").Inc()
			s.audit(ctx, "system", "flag_collusion", p.A+"|"+p.B, map[string]any{"games": p.Games, "score": CollusionScore(p)})
		}
	}

	agents, err := s.repo.AgentsWithSamples(ctx, minTimingSamples)
	if err != nil {
		return err
	}
	for _, ag := range agents {
		t, err := s.repo.AgentTiming(ctx, ag)
		if err != nil {
			continue
		}
		if LooksHuman(t) {
			_ = s.repo.RecordFlag(ctx, ag, "", "human_timing", "timing distribution is human-like")
			s.m.flags.WithLabelValues("human_timing").Inc()
			s.audit(ctx, "system", "flag_human_timing", ag, map[string]any{"mean_ms": t.MeanMs, "std_ms": t.StdMs})
		}
	}
	return nil
}

func (s *Service) audit(ctx context.Context, actor, action, target string, detail map[string]any) {
	b, _ := json.Marshal(detail)
	if err := s.repo.Audit(ctx, actor, action, target, b); err != nil {
		s.log.Error("antifraud: audit write failed", "action", action, "target", target, "error", err)
	}
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	holds prometheus.Counter
	flags *prometheus.CounterVec
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		holds: prometheus.NewCounter(prometheus.CounterOpts{Name: "payout_holds_placed_total", Help: "Payout holds placed by anti-fraud."}),
		flags: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fraud_flags_total", Help: "Fraud flags raised, by type."}, []string{"type"}),
	}
	reg.MustRegister(m.holds, m.flags)
	return m
}
