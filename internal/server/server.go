package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type Probe func(context.Context) error

const ReadinessTimeout = 2 * time.Second

func Handler(control, shard Probe) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), ReadinessTimeout)
		defer cancel()
		results := make(chan error, 2)
		go func() { results <- control(ctx) }()
		go func() { results <- shard(ctx) }()
		for range 2 {
			select {
			case err := <-results:
				if err != nil {
					respond(w, http.StatusServiceUnavailable, "not_ready")
					return
				}
			case <-ctx.Done():
				respond(w, http.StatusServiceUnavailable, "not_ready")
				return
			}
		}
		if ctx.Err() != nil {
			respond(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		respond(w, http.StatusOK, "ready")
	})
	return mux
}

func respond(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"status":"` + value + `"}` + "\n"))
}

func Serve(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) error {
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	stopped := make(chan struct{})
	defer close(stopped)
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := httpServer.Shutdown(shutdownCtx); err != nil {
				logger.Warn("HTTP shutdown deadline reached")
				_ = httpServer.Close()
			}
		case <-stopped:
		}
	}()
	err := httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}
