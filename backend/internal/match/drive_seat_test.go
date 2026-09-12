package match

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
)

type seatMover struct{ connected map[string]bool }

func (m seatMover) Connected(id string) bool { return m.connected[id] }
func (seatMover) Turn(context.Context, string, any, any) error {
	return nil
}
func (seatMover) GameEnd(context.Context, string, string, string, json.RawMessage) error {
	return nil
}

type seatResolver struct{ has bool }

func (r seatResolver) PlayTarget(context.Context, string) (agentclient.Target, bool, error) {
	return agentclient.Target{EndpointURL: "https://hosted.example/play"}, r.has, nil
}

type seatPush struct{ plays int }

func (p *seatPush) Play(context.Context, agentclient.Target, any, any) (int, error) {
	p.plays++
	return 200, nil
}
func (p *seatPush) GameEnd(context.Context, agentclient.Target, agentclient.GameEndNotification) error {
	return nil
}

func TestSeatForPrefersLiveSocketOverHostedHTTP(t *testing.T) {
	d := &driver{
		gw:       seatMover{connected: map[string]bool{"agt_local": true}},
		resolver: seatResolver{has: true},
		client:   &seatPush{},
	}
	sd, ok := d.seatFor(context.Background(), "agt_local", "mt_1")
	if !ok {
		t.Fatal("connected local SDK must be drivable")
	}
	if _, isSock := sd.(socketSeat); !isSock {
		t.Fatalf("connected local SDK must get socket turns, got %T", sd)
	}
}

func TestSeatForHostedHTTPWhenNoSocket(t *testing.T) {
	d := &driver{
		gw:       seatMover{connected: map[string]bool{}},
		resolver: seatResolver{has: true},
		client:   &seatPush{},
	}
	sd, ok := d.seatFor(context.Background(), "agt_hosted", "mt_1")
	if !ok {
		t.Fatal("hosted verify without a local socket must be drivable")
	}
	if _, isHTTP := sd.(httpSeat); !isHTTP {
		t.Fatalf("hosted-only agent must get HTTP turns, got %T", sd)
	}
}

func TestSeatForNeitherReturnsFalse(t *testing.T) {
	d := &driver{gw: seatMover{connected: map[string]bool{}}}
	if _, ok := d.seatFor(context.Background(), "agt_away", "mt_1"); ok {
		t.Fatal("nothing reachable must not invent a seat driver")
	}
}
