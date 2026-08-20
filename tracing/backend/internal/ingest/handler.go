package ingest

import (
	"context"
	"net/http"
	"sync"

	"github.com/agent-arena/pyyol-lens/backend/internal/config"
	"github.com/agent-arena/pyyol-lens/backend/internal/schema"
	"github.com/agent-arena/pyyol-lens/backend/internal/store"
	"github.com/agent-arena/pyyol-lens/backend/internal/stream"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Handler struct {
	Config config.Config
	Store  *store.Store
	Stream *stream.JetStream
}

func (h Handler) IngestBatch(c *fiber.Ctx) error {
	var req schema.EventBatchRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
	}
	if len(req.Events) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "events are required"})
	}
	if len(req.Events) > h.Config.MaxBatchSize {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "batch exceeds configured max size"})
	}

	ingestionID := uuid.NewString()
	clean := make([]schema.TelemetryEvent, 0, len(req.Events))
	failures := make([]schema.ValidationFailure, 0)
	for idx := range req.Events {
		event := req.Events[idx]
		if err := schema.NormalizeAndValidate(&event, h.Config.DefaultProject, h.Config.Environment, h.Config.TelemetryTextCapRunes); err != nil {
			failures = append(failures, schema.ValidationFailure{
				Index:   idx,
				EventID: event.EventID,
				Error:   err.Error(),
			})
			continue
		}
		clean = append(clean, event)
	}

	if len(clean) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(schema.EventBatchResponse{
			Accepted:    0,
			Rejected:    len(failures),
			IngestionID: ingestionID,
			Failures:    failures,
		})
	}

	if err := h.Store.InsertRawEvents(c.UserContext(), ingestionID, clean); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	if h.Stream != nil {
		if err := h.publishToStream(c.UserContext(), ingestionID, clean); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
		}
	}

	status := http.StatusAccepted
	if len(failures) == 0 {
		status = http.StatusOK
	}
	return c.Status(status).JSON(schema.EventBatchResponse{
		Accepted:    len(clean),
		Rejected:    len(failures),
		IngestionID: ingestionID,
		Failures:    failures,
	})
}

func (h Handler) IngestEvents(c *fiber.Ctx) error {
	return h.IngestBatch(c)
}

func (h Handler) publishToStream(ctx context.Context, ingestionID string, events []schema.TelemetryEvent) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(events))
	for i := range events {
		event := events[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Stream.PublishEvent(ctx, ingestionID, event); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
