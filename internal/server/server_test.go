package server

import (
	"context"
	"encoding/json"
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

func TestSwaggerCatalogIsPublicAndComplete(t *testing.T) {
	probe := func(context.Context) error { return nil }
	handler := Handler(probe, probe, probe, nil)

	ui := httptest.NewRecorder()
	handler.ServeHTTP(ui, httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil))
	if ui.Code != http.StatusOK {
		t.Fatalf("swagger UI status = %d, want %d", ui.Code, http.StatusOK)
	}

	document := httptest.NewRecorder()
	handler.ServeHTTP(document, httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil))
	if document.Code != http.StatusOK {
		t.Fatalf("swagger document status = %d, want %d", document.Code, http.StatusOK)
	}
	var spec struct {
		Swagger             string                    `json:"swagger"`
		Paths               map[string]map[string]any `json:"paths"`
		Definitions         map[string]any            `json:"definitions"`
		SecurityDefinitions map[string]any            `json:"securityDefinitions"`
	}
	if err := json.Unmarshal(document.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode swagger document: %v", err)
	}
	if spec.Swagger != "2.0" {
		t.Fatalf("swagger version = %q, want 2.0", spec.Swagger)
	}
	if _, ok := spec.SecurityDefinitions["BearerAuth"]; !ok {
		t.Fatal("swagger document is missing BearerAuth")
	}
	if _, ok := spec.Definitions["server.problemResponse"]; !ok {
		t.Fatal("swagger document is missing the shared problem response schema")
	}

	expected := map[string][]string{
		"/health/live": {"get"}, "/health/ready": {"get"},
		"/v1/auth/login": {"post"}, "/v1/auth/refresh": {"post"}, "/v1/auth/logout": {"post"},
		"/v1/me": {"get"}, "/v1/me/password": {"post"},
		"/v1/banks": {"get", "post"}, "/v1/banks/{bankID}": {"get", "patch"},
		"/v1/users": {"get", "post"}, "/v1/users/{userID}": {"get", "patch"},
		"/v1/users/{userID}:disable": {"post"}, "/v1/users/{userID}:set-password": {"post"},
		"/v1/banks/{bankID}/card-products":                                             {"get", "post"},
		"/v1/banks/{bankID}/card-products/{productID}":                                 {"get", "patch"},
		"/v1/banks/{bankID}/clients":                                                   {"get", "post"},
		"/v1/banks/{bankID}/clients/{clientID}":                                        {"get", "patch"},
		"/v1/banks/{bankID}/account-references":                                        {"get", "post"},
		"/v1/banks/{bankID}/account-references/{accountReferenceID}":                   {"get", "patch"},
		"/v1/banks/{bankID}/cards":                                                     {"get", "post"},
		"/v1/banks/{bankID}/cards/{cardID}":                                            {"get"},
		"/v1/banks/{bankID}/cards/{cardID}/operations":                                 {"get"},
		"/v1/banks/{bankID}/cards/{cardID}/history":                                    {"get"},
		"/v1/banks/{bankID}/cards/{cardID}:activate":                                   {"post"},
		"/v1/banks/{bankID}/cards/{cardID}:suspend":                                    {"post"},
		"/v1/banks/{bankID}/cards/{cardID}:resume":                                     {"post"},
		"/v1/banks/{bankID}/cards/{cardID}:close":                                      {"post"},
		"/v1/banks/{bankID}/cards/{cardID}:replace":                                    {"post"},
		"/v1/banks/{bankID}/card-status-batches":                                       {"get", "post"},
		"/v1/banks/{bankID}/card-status-batches/{batchID}":                             {"get"},
		"/v1/banks/{bankID}/card-status-batches/{batchID}/items":                       {"get"},
		"/v1/banks/{bankID}/card-status-batches/{batchID}:execute":                     {"post"},
		"/v1/banks/{bankID}/card-status-batches/{batchID}:cancel":                      {"post"},
		"/v1/banks/{bankID}/card-status-batches/{batchID}:retry":                       {"post"},
		"/v1/banks/{bankID}/card-expiry-runs/{expiryRunID}/items/{expiryItemID}:retry": {"post"},
	}
	operations := 0
	for path, methods := range expected {
		documented, ok := spec.Paths[path]
		if !ok {
			t.Fatalf("swagger document is missing path %s", path)
		}
		for _, method := range methods {
			operations++
			if _, ok := documented[method]; !ok {
				t.Fatalf("swagger document is missing %s %s", method, path)
			}
		}
	}
	if operations != 47 {
		t.Fatalf("documented operation count = %d, want 47", operations)
	}
	if _, found := spec.Paths["/v1/authorization-verifications"]; found {
		t.Fatal("mTLS authorization endpoint must remain outside the public swagger catalog")
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
