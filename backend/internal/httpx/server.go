package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/config"
)

// Server wraps http.Server with sane timeouts and graceful shutdown.
type Server struct {
	httpServer *http.Server
	log        *slog.Logger
	grace      time.Duration
}

// NewServer constructs the HTTP server with timeouts from config.
func NewServer(cfg *config.Config, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
			Handler:           handler,
			ReadTimeout:       cfg.ReadTimeout,
			ReadHeaderTimeout: cfg.ReadTimeout,
			// WriteTimeout is deliberately 0: an absolute per-connection write
			// deadline set at request start force-closes SSE streams and long-polls
			// (they outlive it). A bounded write deadline is instead applied per
			// request by middleware.WriteDeadline(cfg.WriteTimeout) for ordinary
			// handlers, and re-armed as a rolling deadline by httpx.ArmWriteDeadline
			// on the streaming/long-poll paths.
			WriteTimeout: 0,
			IdleTimeout:  cfg.IdleTimeout,
		},
		log:   log,
		grace: cfg.ShutdownGrace,
	}
}

// Run serves until ctx is cancelled (e.g. on SIGTERM), then drains in-flight
// requests within the grace window before returning. A failed listen returns
// immediately.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutdown signal received; draining", "grace", s.grace.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.grace)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			s.log.Error("graceful shutdown failed; forcing close", "error", err)
			return s.httpServer.Close()
		}
		s.log.Info("http server stopped cleanly")
		return nil
	}
}
