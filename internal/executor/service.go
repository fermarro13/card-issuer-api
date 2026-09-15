package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Service coordinates bounded, independently deployable asynchronous work.
type Service struct {
	config    Config
	store     Store
	directory Directory
	now       func() time.Time

	mu       sync.Mutex
	rotation int
	byBank   map[string]int
	inFlight int
	workers  sync.WaitGroup

	claims     atomic.Int64
	retries    atomic.Int64
	recoveries atomic.Int64
	succeeded  atomic.Int64
	failed     atomic.Int64
	loopOK     atomic.Int64
}

func New(config Config, store Store, directory Directory) *Service {
	return &Service{config: config, store: store, directory: directory, now: time.Now, byBank: make(map[string]int)}
}

// Run claims no new work after cancellation and waits a bounded time for each
// already-fenced transaction. Interrupted work is intentionally recoverable.
func (s *Service) Run(ctx context.Context) error {
	if err := s.cycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			drained := make(chan struct{})
			go func() { s.workers.Wait(); close(drained) }()
			select {
			case <-drained:
				return nil
			case <-time.After(s.config.DrainTimeout):
				return nil
			}
		case <-ticker.C:
			if err := s.cycle(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
	}
}

func (s *Service) cycle(ctx context.Context) error {
	routes, err := s.activeBanks(ctx)
	if err != nil {
		return err
	}
	today := s.now().UTC().Truncate(24 * time.Hour)
	for _, bank := range routes {
		if err := s.store.EnsureExpiryRun(ctx, bank, today, s.config.Identity); err != nil {
			return fmt.Errorf("ensure expiry run: %w", err)
		}
		count, recoverErr := s.store.RecoverBatches(ctx, Claim{BankID: bank, Owner: s.config.Identity, LeaseDuration: s.config.LeaseDuration})
		if recoverErr != nil {
			return fmt.Errorf("recover batch leases: %w", recoverErr)
		}
		s.recoveries.Add(int64(count))
		count, recoverErr = s.store.RecoverExpiryItems(ctx, Claim{BankID: bank, Owner: s.config.Identity, LeaseDuration: s.config.LeaseDuration})
		if recoverErr != nil {
			return fmt.Errorf("recover expiry leases: %w", recoverErr)
		}
		s.recoveries.Add(int64(count))
	}
	for _, bank := range s.rotate(routes) {
		if !s.reserve(bank) {
			continue
		}
		claim, claimErr := s.store.ClaimBatch(ctx, Claim{BankID: bank, Owner: s.config.Identity, LeaseDuration: s.config.LeaseDuration})
		if claimErr != nil {
			s.release(bank)
			return fmt.Errorf("claim batch: %w", claimErr)
		}
		if claim != nil {
			s.claims.Add(1)
			s.workers.Add(1)
			go s.runBatch(context.WithoutCancel(ctx), *claim)
		} else {
			s.release(bank)
		}
		if !s.reserve(bank) {
			continue
		}
		item, itemErr := s.store.ClaimExpiryItem(ctx, Claim{BankID: bank, Owner: s.config.Identity, LeaseDuration: s.config.LeaseDuration})
		if itemErr != nil {
			s.release(bank)
			return fmt.Errorf("claim expiry item: %w", itemErr)
		}
		if item != nil {
			s.claims.Add(1)
			s.workers.Add(1)
			go s.runExpiry(context.WithoutCancel(ctx), *item)
		} else {
			s.release(bank)
		}
	}
	s.loopOK.Add(1)
	return nil
}

func (s *Service) runBatch(ctx context.Context, batch Batch) {
	defer s.workers.Done()
	defer s.release(batch.BankID)
	if batch.ItemCount > s.config.BatchSizeCap {
		_ = s.store.FailBatch(ctx, batch, s.config.Identity, "invalid_transition")
		s.failed.Add(1)
		return
	}
	allowed, retryable := s.authorized(ctx, batch)
	if retryable {
		s.retryBatch(ctx, batch, "control_unavailable", "Batch authorization will be retried.")
		return
	}
	if !allowed {
		_ = s.store.FailBatch(ctx, batch, s.config.Identity, "authorization_revoked")
		s.failed.Add(1)
		return
	}
	err := s.store.ApplyBatch(ctx, batch, s.config.Identity)
	if err == nil {
		s.succeeded.Add(1)
		return
	}
	if errors.Is(err, ErrRetryable) || IsTransient(err) {
		s.retryBatch(ctx, batch, "dependency_unavailable", "The batch will be retried.")
		return
	}
	if errors.Is(err, ErrStale) {
		return
	}
	_ = s.store.FailBatch(ctx, batch, s.config.Identity, "invalid_transition")
	s.failed.Add(1)
}

func (s *Service) retryBatch(ctx context.Context, batch Batch, code, summary string) {
	if batch.AttemptCount >= s.config.BatchMaxAttempts {
		_ = s.store.FailBatch(ctx, batch, s.config.Identity, "retry_exhausted")
		s.failed.Add(1)
		return
	}
	if s.store.RequeueBatch(ctx, batch, s.config.Identity, code, batchBackoff(s.config.BatchInitialBackoff, s.config.BatchMaximumBackoff, batch.AttemptCount)) == nil {
		s.retries.Add(1)
	}
	_ = summary // summaries are repository allow-list values, never source errors.
}

func (s *Service) runExpiry(ctx context.Context, item ExpiryItem) {
	defer s.workers.Done()
	defer s.release(item.BankID)
	err := s.store.ApplyExpiryItem(ctx, item, s.config.Identity)
	if err == nil {
		s.succeeded.Add(1)
		return
	}
	if errors.Is(err, ErrRetryable) || IsTransient(err) {
		if s.store.RequeueExpiryItem(ctx, item, s.config.Identity, s.config.ExpiryInitialBackoff) == nil {
			s.retries.Add(1)
		}
		return
	}
	if errors.Is(err, ErrStale) {
		return
	}
	_ = s.store.FailExpiryItem(ctx, item, s.config.Identity)
	s.failed.Add(1)
}

func (s *Service) authorized(ctx context.Context, batch Batch) (allowed, retryable bool) {
	user, err := s.directory.User(ctx, batch.RequestedBy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false
		}
		return false, true
	}
	routes, err := s.directory.Banks(ctx)
	if err != nil {
		return false, true
	}
	if user.Status != "enabled" {
		return false, false
	}
	if user.Role == "bank_operator" && user.EntityID != batch.BankID {
		return false, false
	}
	if user.Role != "issuer_operator" && user.Role != "bank_operator" {
		return false, false
	}
	for _, route := range routes {
		if route.EntityID == batch.BankID && route.ShardID == s.config.ShardID && route.PlacementStatus == "active" {
			return true, false
		}
	}
	return false, false
}

func (s *Service) activeBanks(ctx context.Context) ([]string, error) {
	routes, err := s.directory.Banks(ctx)
	if err != nil {
		return nil, err
	}
	banks := make([]string, 0, len(routes))
	for _, route := range routes {
		if route.ShardID == s.config.ShardID && route.PlacementStatus == "active" {
			banks = append(banks, route.EntityID)
		}
	}
	sort.Strings(banks)
	return banks, nil
}

func (s *Service) rotate(banks []string) []string {
	if len(banks) < 2 {
		return banks
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	start := s.rotation % len(banks)
	s.rotation = (s.rotation + 1) % len(banks)
	return append(append([]string(nil), banks[start:]...), banks[:start]...)
}

func (s *Service) reserve(bank string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight >= s.config.MaxClaims || s.byBank[bank] >= s.config.PerBankConcurrency {
		return false
	}
	s.inFlight++
	s.byBank[bank]++
	return true
}
func (s *Service) release(bank string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	s.byBank[bank]--
}

type Snapshot struct {
	Claims, Retries, Recoveries, Succeeded, Failed, Loops int64
	InFlight                                              int
}

func (s *Service) Metrics() Snapshot {
	s.mu.Lock()
	inFlight := s.inFlight
	s.mu.Unlock()
	return Snapshot{s.claims.Load(), s.retries.Load(), s.recoveries.Load(), s.succeeded.Load(), s.failed.Load(), s.loopOK.Load(), inFlight}
}

func IsTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", "40P01", "55P03", "57P01":
			return true
		}
		return len(pgErr.Code) >= 2 && pgErr.Code[:2] == "08"
	}
	var networkErr net.Error
	return errors.As(err, &networkErr) && (networkErr.Timeout() || networkErr.Temporary())
}
