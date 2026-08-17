// Package webhook owns the optional HTTP surface for health, metrics and
// provider webhooks.
//
//nolint:wsl_v5 // HTTP lifecycle code is easier to scan in compact blocks.
package webhook

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const (
	DefaultMaxBodyBytes = 64 * 1024

	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
)

// Config controls the optional HTTP server.
type Config struct {
	ListenAddr        string
	MetricsEnabled    bool
	TributeEnabled    bool
	TelegramEnabled   bool
	TributePath       string
	TelegramPath      string
	MaxBodyBytes      int64
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	ShutdownTimeout   time.Duration
	Readiness         Readiness
	Metrics           *Metrics
	Tribute           http.Handler
	Telegram          http.Handler
	Logger            *slog.Logger
}

// Server is an optional HTTP runtime.
type Server struct {
	cfg     Config
	handler http.Handler
}

// NewServer builds an HTTP server with only enabled routes mounted.
func NewServer(cfg Config) *Server {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}

	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}

	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = defaultReadHeaderTimeout
	}

	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}

	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultShutdownTimeout
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	s := &Server{cfg: cfg}
	s.handler = s.buildMux()

	return s
}

// Handler exposes the route-gated mux for tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run serves until ctx is cancelled, then performs graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	httpServer := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		ReadTimeout:       s.cfg.ReadTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		err := httpServer.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}

		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}

		return <-errCh
	}
}

func (s *Server) buildMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", methodHandler(http.MethodGet, s.healthz))
	mux.HandleFunc("/livez", methodHandler(http.MethodGet, s.livez))
	mux.HandleFunc("/readyz", methodHandler(http.MethodGet, s.readyz))

	if s.cfg.MetricsEnabled {
		mux.HandleFunc("/metrics", methodHandler(http.MethodGet, s.metrics))
	}

	if s.cfg.TributeEnabled && s.cfg.Tribute != nil {
		mux.Handle(s.cfg.TributePath, methodHandler(
			http.MethodPost,
			limitBody(s.cfg.MaxBodyBytes, s.cfg.Tribute),
		))
	}

	if s.cfg.TelegramEnabled && s.cfg.Telegram != nil {
		mux.Handle(s.cfg.TelegramPath, methodHandler(
			http.MethodPost,
			limitBody(s.cfg.MaxBodyBytes, s.cfg.Telegram),
		))
	}

	return mux
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// livez reports process-local liveness only: background subsystems that must
// be running. It performs no database or network access, and that is what
// separates it from /readyz, which is deliberately strict and may flap on a
// transient Telegram or reconcile hiccup. /healthz keeps its narrower meaning:
// the HTTP server itself is up.
func (s *Server) livez(w http.ResponseWriter, _ *http.Request) {
	alive := s.cfg.Readiness.EnforcerAlive
	if alive == nil || alive() {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})

		return
	}

	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status": "not_live",
		"failed": []string{"enforcer"},
	})
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	result := s.cfg.Readiness.Check(r.Context())
	if result.Ready {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})

		return
	}

	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status": "not_ready",
		"failed": result.Failed,
	})
}

// metrics answers 200 even when the store read fails part-way, and that is
// deliberate: a 5xx would throw away the in-process families (worker pool,
// Telegram errors) that Metrics.Write emits first, exactly when the database is
// the thing misbehaving. The partial body is not silent either — it carries
// `gatekeeper_metrics_store_scrape_success 0`, so the failure is a series
// Prometheus can alert on rather than a line only in this log.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	metrics := s.cfg.Metrics
	if metrics == nil {
		metrics = &Metrics{}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := metrics.Write(r.Context(), w); err != nil {
		s.cfg.Logger.Error("metrics scrape read the store incompletely",
			slog.Any("error", err))
	}
}

func methodHandler(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		handler(w, r)
	}
}

func limitBody(maxBytes int64, handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		handler.ServeHTTP(w, r)
	}
}
