package mafia

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
)

// Mafia push-play: a no-stakes 12-seat table where the developer's seat is driven
// by their hosted agent endpoint (manifest push model) and the other 11 seats are
// filled and driven by deterministic rule-based bots — so a single developer can
// exercise a full Mafia match solo, watched live over the normal SSE/state
// endpoints. This is also what makes a solo Mafia practice table actually run
// (Mafia needs a full roster, unlike Goofspiel/Monopoly).

// BotAgent is one seedable opponent identity (a demo agent) used to fill seats.
type BotAgent struct {
	PublicID      string
	OwnerPublicID string
}

// RemoteResolver returns the push-play Target for an agent's verified manifest.
type RemoteResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// PushClient POSTs a game view to the developer's endpoint and decodes the move.
type PushClient interface {
	Play(ctx context.Context, t agentclient.Target, request, out any) (int, error)
}

type pushPlayer struct {
	remote   RemoteResolver
	client   PushClient
	bots     []BotAgent
	log      *slog.Logger
	maxMatch time.Duration
}

// EnablePushPlay wires POST /v1/mafia/pushplay. bots must have at least
// RosterSize-1 entries to fill a table; fewer disables push-play.
func (s *Service) EnablePushPlay(remote RemoteResolver, client PushClient, bots []BotAgent, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	s.pusher = &pushPlayer{remote: remote, client: client, bots: bots, log: log, maxMatch: 5 * time.Minute}
}

// MafiaPushView is the redacted per-seat JSON POSTed to the agent endpoint. It
// carries only what this seat legitimately knows.
type MafiaPushView struct {
	Game     string       `json:"game"` // "mafia"
	MatchID  string       `json:"match_id"`
	YourSeat int          `json:"your_seat"`
	YourRole string       `json:"your_role,omitempty"`
	Day      int          `json:"day"`
	Phase    string       `json:"phase"`
	Alive    map[int]bool `json:"alive"`
	Allies   []int        `json:"allies,omitempty"`
	Legal    []string     `json:"legal"`
	Public   []mf.Event   `json:"public,omitempty"`
	Private  []mf.Event   `json:"private,omitempty"`
}

// MafiaPushMove is the action the agent returns.
type MafiaPushMove struct {
	Action string `json:"action"`
	Target int    `json:"target"`
	Tone   string `json:"tone"`
	Text   string `json:"text"`
}

// StartPushPlay opens a no-stakes table (creator seat 1), fills the rest with
// bots to start the match, and drives seat 1 from the developer's endpoint.
func (s *Service) StartPushPlay(ctx context.Context, userAgent, userOwner string) (string, error) {
	if s.pusher == nil || len(s.pusher.bots) < s.cfg.RosterSize-1 {
		return "", httpx.NewError(501, "pushplay_unavailable", "Mafia push-play is not configured on this server.")
	}
	target, found, err := s.pusher.remote.PlayTarget(ctx, userAgent)
	if err != nil {
		return "", err
	}
	if !found || target.EndpointURL == "" {
		return "", httpx.NewError(400, "no_verified_endpoint",
			"Register and verify your agent's endpoint (manifest) before running push-play.")
	}

	matchID, err := s.CreateTable(ctx, userAgent, userOwner, 0)
	if err != nil {
		s.pusher.log.Error("mafia pushplay: create table failed", "err", err)
		return "", err
	}
	// Fill the remaining seats with bots; the final join starts the match.
	seatIDs := []string{userAgent}
	for i := 0; i < s.cfg.RosterSize-1; i++ {
		b := s.pusher.bots[i]
		if _, err := s.Join(ctx, b.PublicID, b.OwnerPublicID, matchID); err != nil {
			s.pusher.log.Error("mafia pushplay: bot join failed", "seat", i+2, "bot", b.PublicID, "err", err)
			return "", err
		}
		seatIDs = append(seatIDs, b.PublicID)
	}

	go s.pusher.drive(s, matchID, userAgent, target, seatIDs)
	return matchID, nil
}

// drive advances the match: each pass, every alive seat with a pending action
// acts — seat 1 (userAgent) via the endpoint, the rest via a rule-based bot. The
// engine validates each move, so a stale action (phase already advanced) is
// harmlessly rejected and retried next pass.
func (p *pushPlayer) drive(s *Service, matchID, userAgent string, target agentclient.Target, seatIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), p.maxMatch)
	defer cancel()

	for {
		if ctx.Err() != nil {
			p.log.Warn("mafia pushplay: deadline exceeded", "match", matchID)
			return
		}
		base, err := s.State(ctx, matchID, userAgent)
		if err != nil {
			p.log.Warn("mafia pushplay: state read failed", "match", matchID, "err", err)
			return
		}
		if base.Status != StatusActive {
			p.log.Info("mafia pushplay: match finished", "match", matchID, "status", base.Status)
			return
		}

		acted := false
		for _, id := range seatIDs {
			v, err := s.State(ctx, matchID, id)
			if err != nil || v.Status != StatusActive || len(v.Legal) == 0 {
				continue
			}
			var act mf.Action
			if id == userAgent {
				act = p.decideRemote(ctx, target, matchID, v)
			} else {
				act = botDecide(v)
			}
			if _, err := s.Act(ctx, id, matchID, act); err == nil {
				acted = true
			}
		}
		if !acted {
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (p *pushPlayer) decideRemote(ctx context.Context, target agentclient.Target, matchID string, v AgentView) mf.Action {
	req := MafiaPushView{
		Game: "mafia", MatchID: matchID, YourSeat: v.YourSeat, YourRole: v.YourRole,
		Day: v.Day, Phase: v.Phase, Alive: v.Alive, Allies: v.Allies, Legal: v.Legal,
		Public: v.Public, Private: v.Private,
	}
	var move MafiaPushMove
	if _, err := p.client.Play(ctx, target, req, &move); err == nil && containsStr(v.Legal, move.Action) {
		return mf.Action{Kind: move.Action, Target: move.Target, Tone: move.Tone, Text: move.Text}
	}
	return botDecide(v) // endpoint failed or returned an illegal action
}

// botDecide is a deterministic rule-based move for the current phase — the same
// baseline the demo runner uses, kept in-package to avoid an import cycle.
func botDecide(v AgentView) mf.Action {
	kind := ""
	if len(v.Legal) > 0 {
		kind = v.Legal[0] // exactly one legal kind per seat per phase
	}
	target := firstOtherAlive(v.Alive, v.YourSeat)
	switch kind {
	case "message":
		return mf.Action{Kind: "message", Tone: "info", Text: "Observing the table.", Target: target}
	case "protect":
		return mf.Action{Kind: "protect", Target: v.YourSeat} // doctor may guard itself
	case "":
		return mf.Action{}
	default: // night_kill | investigate | profile | vote
		return mf.Action{Kind: kind, Target: target}
	}
}

func firstOtherAlive(alive map[int]bool, seat int) int {
	best := 0
	for s, ok := range alive {
		if ok && s != seat && (best == 0 || s < best) {
			best = s
		}
	}
	return best
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
