package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
)

type JetStream struct {
	conn *nats.Conn
	js   nats.JetStreamContext
	cfg  config.Config
}

func New(cfg config.Config) (*JetStream, error) {
	conn, err := connectWithRetry(cfg)
	if err != nil {
		return nil, err
	}
	js, err := conn.JetStream()
	if err != nil {
		_ = conn.Drain()
		return nil, err
	}
	s := &JetStream{conn: conn, js: js, cfg: cfg}
	if err := s.ensureStream(); err != nil {
		_ = conn.Drain()
		return nil, err
	}
	return s, nil
}

func connectWithRetry(cfg config.Config) (*nats.Conn, error) {
	const (
		maxAttempts = 20
		retryDelay  = 1 * time.Second
	)

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		conn, err := nats.Connect(
			cfg.NATSURL,
			nats.Name("pyyol-lens"),
			nats.Timeout(2*time.Second),
		)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(retryDelay)
	}

	return nil, fmt.Errorf(
		"failed to connect to NATS at %q after %d attempts: %w. Make sure NATS is running (for local setup run: docker compose up -d nats)",
		cfg.NATSURL,
		maxAttempts,
		lastErr,
	)
}

func (s *JetStream) Close() error {
	if s.conn == nil {
		return nil
	}
	return s.conn.Drain()
}

func (s *JetStream) ensureStream() error {
	_, err := s.js.StreamInfo(s.cfg.NATSStreamName)
	if err == nil {
		return nil
	}
	_, err = s.js.AddStream(&nats.StreamConfig{
		Name:      s.cfg.NATSStreamName,
		Subjects:  []string{s.cfg.NATSSubjectEvents + ".>"},
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Discard:   nats.DiscardOld,
		MaxAge:    7 * 24 * time.Hour,
		Replicas:  1,
	})
	return err
}

func (s *JetStream) PublishEvent(ctx context.Context, ingestionID string, event schema.TelemetryEvent) error {
	payload, err := json.Marshal(struct {
		IngestionID string                `json:"ingestion_id"`
		Event       schema.TelemetryEvent `json:"event"`
	}{
		IngestionID: ingestionID,
		Event:       event,
	})
	if err != nil {
		return err
	}
	subject := fmt.Sprintf(
		"%s.%s.%s.%s",
		s.cfg.NATSSubjectEvents,
		safe(event.OrganizationID),
		safe(event.ProjectID),
		safe(event.Environment),
	)
	_, err = s.js.PublishMsg(&nats.Msg{
		Subject: subject,
		Data:    payload,
		Header: nats.Header{
			"pl-event-id": []string{event.EventID},
			"pl-trace-id": []string{event.TraceID},
		},
	})
	return err
}

func (s *JetStream) EnsureDurableConsumer() error {
	_, err := s.js.ConsumerInfo(s.cfg.NATSStreamName, s.cfg.NATSConsumerName)
	if err == nil {
		return nil
	}
	_, err = s.js.AddConsumer(s.cfg.NATSStreamName, &nats.ConsumerConfig{
		Durable:       s.cfg.NATSConsumerName,
		AckPolicy:     nats.AckExplicitPolicy,
		DeliverPolicy: nats.DeliverAllPolicy,
		AckWait:       30 * time.Second,
		MaxAckPending: 2048,
		FilterSubject: s.cfg.NATSSubjectEvents + ".>",
		ReplayPolicy:  nats.ReplayInstantPolicy,
	})
	return err
}

func (s *JetStream) PullSubscribe() (*nats.Subscription, error) {
	return s.js.PullSubscribe(
		s.cfg.NATSSubjectEvents+".>",
		s.cfg.NATSConsumerName,
		nats.BindStream(s.cfg.NATSStreamName),
		nats.ManualAck(),
	)
}

func safe(in string) string {
	if in == "" {
		return "unknown"
	}
	out := make([]rune, 0, len(in))
	for _, ch := range in {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' {
			out = append(out, ch)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
