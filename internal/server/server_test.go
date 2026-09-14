package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLivenessDoesNotCallDatabases(t *testing.T) {
	probe := func(context.Context) error { t.Error("liveness called database"); return errors.New("unavailable") }
	w := httptest.NewRecorder()
	Handler(probe, probe, probe, nil).ServeHTTP(w, httptest.NewRequest("GET", "/health/live", nil))
	if w.Code != 200 || w.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected liveness response: %d %s", w.Code, w.Body)
	}
}

func TestReadinessOutageAndRecovery(t *testing.T) {
	var offline atomic.Bool
	control := func(context.Context) error {
		if offline.Load() {
			return errors.New("internal connection secret")
		}
		return nil
	}
	var shardCalls atomic.Int32
	shard := func(context.Context) error { shardCalls.Add(1); return nil }
	handler := Handler(control, control, shard, nil)
	for _, down := range []bool{false, true, false} {
		offline.Store(down)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
		status, body := 200, "{\"status\":\"ready\"}\n"
		if down {
			status, body = 503, "{\"status\":\"not_ready\"}\n"
		}
		if w.Code != status || w.Body.String() != body || w.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected readiness response: %d %s", w.Code, w.Body)
		}
	}
	if shardCalls.Load() < 2 {
		t.Fatal("healthy readiness must check both pools")
	}
}

func TestReadinessRequiresAuthenticationPool(t *testing.T) {
	authUnavailable := func(context.Context) error { return errors.New("unavailable") }
	w := httptest.NewRecorder()
	Handler(authUnavailable, func(context.Context) error { return nil }, func(context.Context) error { return nil }, nil).ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("authentication outage was ready: %d", w.Code)
	}
}

func TestReadinessUsesOneConcurrentTwoSecondDeadline(t *testing.T) {
	started := make(chan time.Time, 3)
	probe := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("missing deadline")
		}
		started <- deadline
		<-ctx.Done()
		return ctx.Err()
	}
	handler := Handler(probe, probe, probe, nil)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	begin := time.Now()
	go func() { handler.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil)); close(done) }()
	var deadlines []time.Time
	for range 3 {
		select {
		case d := <-started:
			deadlines = append(deadlines, d)
		case <-time.After(time.Second):
			t.Fatal("database checks did not start concurrently")
		}
	}
	if !deadlines[0].Equal(deadlines[1]) || !deadlines[1].Equal(deadlines[2]) {
		t.Fatal("checks must share a deadline")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("readiness exceeded its timeout")
	}
	if w.Code != 503 || time.Since(begin) > 3*time.Second {
		t.Fatal("readiness did not fail within the shared deadline")
	}
}

func TestReadinessCancellation(t *testing.T) {
	probe := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	Handler(probe, probe, probe, nil).ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil).WithContext(ctx))
	if w.Code != 503 {
		t.Fatal("cancelled request was ready")
	}
}

func TestGracefulShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- Serve(ctx, listener, Handler(func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil }, nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	client := http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop gracefully")
	}
}
