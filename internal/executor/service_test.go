package executor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRoundRobinAndConcurrencyCaps(t *testing.T) {
	service := New(testConfig(), &fakeStore{}, fakeDirectory{})
	first := service.rotate([]string{"a", "b", "c"})
	second := service.rotate([]string{"a", "b", "c"})
	if first[0] != "a" || second[0] != "b" {
		t.Fatalf("round robin = %v then %v", first, second)
	}
	if !service.reserve("a") || !service.reserve("b") || service.reserve("a") {
		t.Fatal("concurrency caps were not enforced")
	}
	service.release("a")
	service.release("b")
}

func TestBatchBackoff(t *testing.T) {
	if got := batchBackoff(time.Second, 8*time.Second, 1); got != time.Second {
		t.Fatalf("first backoff = %s", got)
	}
	if got := batchBackoff(time.Second, 8*time.Second, 4); got != 8*time.Second {
		t.Fatalf("bounded backoff = %s", got)
	}
}

func TestTransientClassification(t *testing.T) {
	if !IsTransient(&pgconn.PgError{Code: "40001"}) {
		t.Fatal("serialization failure must be retryable")
	}
	if IsTransient(&pgconn.PgError{Code: "23514"}) {
		t.Fatal("constraint violation must not be retryable")
	}
}

func TestAuthorizationRevalidation(t *testing.T) {
	config := testConfig()
	batch := Batch{BankID: "bank-a", RequestedBy: "user-a"}
	service := New(config, &fakeStore{}, fakeDirectory{user: domain.DirectoryUser{ID: "user-a", Role: "bank_operator", EntityID: "bank-a", Status: "enabled"}, routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}})
	allowed, retryable := service.authorized(context.Background(), batch)
	if !allowed || retryable {
		t.Fatal("active assigned operator should be authorized")
	}
	service.directory = fakeDirectory{userErr: errors.New("unavailable")}
	allowed, retryable = service.authorized(context.Background(), batch)
	if allowed || !retryable {
		t.Fatal("control failure must be retryable")
	}
	service.directory = fakeDirectory{user: domain.DirectoryUser{ID: "user-a", Role: "bank_operator", EntityID: "bank-b", Status: "enabled"}, routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}}
	allowed, retryable = service.authorized(context.Background(), batch)
	if allowed || retryable {
		t.Fatal("wrong assignment must fail terminally")
	}
}

func TestBatchRetryAndTerminalAuthorizationFailure(t *testing.T) {
	store := &fakeStore{applyErr: ErrRetryable}
	service := New(testConfig(), store, fakeDirectory{user: domain.DirectoryUser{ID: "user-a", Role: "issuer_operator", Status: "enabled"}, routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}})
	if !service.reserve("bank-a") {
		t.Fatal("could not reserve worker")
	}
	service.workers.Add(1)
	service.runBatch(context.Background(), Batch{BankID: "bank-a", RequestedBy: "user-a", AttemptCount: 1})
	if store.requeues != 1 || service.Metrics().Retries != 1 {
		t.Fatal("retryable batch was not requeued")
	}
	service.directory = fakeDirectory{user: domain.DirectoryUser{ID: "user-a", Role: "issuer_readonly", Status: "enabled"}, routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}}
	if !service.reserve("bank-a") {
		t.Fatal("could not reserve worker")
	}
	service.workers.Add(1)
	service.runBatch(context.Background(), Batch{BankID: "bank-a", RequestedBy: "user-a"})
	if store.failures != 1 {
		t.Fatal("authorization loss did not fail the batch")
	}
}

func TestCurrentDateExpiryScheduling(t *testing.T) {
	store := &fakeStore{}
	service := New(testConfig(), store, fakeDirectory{routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}})
	service.now = func() time.Time { return time.Date(2026, 9, 15, 17, 22, 0, 0, time.FixedZone("test", -6*3600)) }
	if err := service.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.expiryDays) != 1 || store.expiryDays[0] != "2026-09-15" {
		t.Fatalf("expiry date = %v", store.expiryDays)
	}
}

func TestGracefulDrainWaitsForWorker(t *testing.T) {
	store := &fakeStore{applyBlock: make(chan struct{})}
	service := New(testConfig(), store, fakeDirectory{user: domain.DirectoryUser{ID: "user-a", Role: "issuer_operator", Status: "enabled"}, routes: []domain.Route{{EntityID: "bank-a", ShardID: "shard-a", PlacementStatus: "active"}}})
	if !service.reserve("bank-a") {
		t.Fatal("could not reserve worker")
	}
	service.workers.Add(1)
	go service.runBatch(context.Background(), Batch{BankID: "bank-a", RequestedBy: "user-a"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	cancel()
	select {
	case <-done:
		t.Fatal("run returned before in-flight work drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(store.applyBlock)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not drain")
	}
}

func testConfig() Config {
	return Config{Identity: "executor-a", ShardID: "shard-a", PollInterval: time.Hour, MaxClaims: 2, PerBankConcurrency: 1, LeaseDuration: time.Second, BatchMaxAttempts: 3, BatchInitialBackoff: time.Second, BatchMaximumBackoff: 8 * time.Second, BatchSizeCap: 200, ExpiryInitialBackoff: time.Second, DrainTimeout: time.Second}
}

type fakeDirectory struct {
	user               domain.DirectoryUser
	routes             []domain.Route
	userErr, routesErr error
}

func (d fakeDirectory) User(context.Context, string) (domain.DirectoryUser, error) {
	return d.user, d.userErr
}
func (d fakeDirectory) Banks(context.Context) ([]domain.Route, error) { return d.routes, d.routesErr }

type fakeStore struct {
	mu                 sync.Mutex
	applyErr           error
	applyBlock         chan struct{}
	requeues, failures int
	expiryDays         []string
}

func (s *fakeStore) ClaimBatch(context.Context, Claim) (*Batch, error)  { return nil, nil }
func (s *fakeStore) RecoverBatches(context.Context, Claim) (int, error) { return 0, nil }
func (s *fakeStore) ApplyBatch(_ context.Context, _ Batch, _ string) error {
	if s.applyBlock != nil {
		<-s.applyBlock
	}
	return s.applyErr
}
func (s *fakeStore) RequeueBatch(context.Context, Batch, string, string, time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requeues++
	return nil
}
func (s *fakeStore) FailBatch(context.Context, Batch, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	return nil
}
func (s *fakeStore) EnsureExpiryRun(_ context.Context, _ string, day time.Time, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expiryDays = append(s.expiryDays, day.Format("2006-01-02"))
	return nil
}
func (s *fakeStore) RecoverExpiryItems(context.Context, Claim) (int, error)      { return 0, nil }
func (s *fakeStore) ClaimExpiryItem(context.Context, Claim) (*ExpiryItem, error) { return nil, nil }
func (s *fakeStore) ApplyExpiryItem(context.Context, ExpiryItem, string) error   { return nil }
func (s *fakeStore) RequeueExpiryItem(context.Context, ExpiryItem, string, time.Duration) error {
	return nil
}
func (s *fakeStore) FailExpiryItem(context.Context, ExpiryItem, string) error { return nil }
