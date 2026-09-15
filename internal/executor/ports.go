// Package executor owns asynchronous batch and expiry execution.
package executor

import (
	"context"
	"errors"
	"time"

	domain "card-issuer-api/internal/domain"
)

var (
	ErrRetryable = errors.New("executor retryable dependency failure")
	ErrStale     = errors.New("executor stale lease")
)

type Batch struct {
	ID              string
	BankID          string
	TargetStatus    string
	Reason          string
	RequestedBy     string
	RequesterRole   string
	RequesterBankID string
	RequestID       string
	ItemCount       int
	AttemptCount    int
	LeaseVersion    int64
}

type ExpiryItem struct {
	ID             string
	RunID          string
	BankID         string
	CardID         string
	OperationID    string
	LeaseVersion   int64
	AutomaticRetry int
}

type Claim struct {
	BankID        string
	Owner         string
	LeaseDuration time.Duration
}

// Store is intentionally executor-owned. Repository adapters may expose more
// implementation detail, but the executor can only claim and fence work.
type Store interface {
	ClaimBatch(context.Context, Claim) (*Batch, error)
	RecoverBatches(context.Context, Claim) (int, error)
	ApplyBatch(context.Context, Batch, string) error
	RequeueBatch(context.Context, Batch, string, string, time.Duration) error
	FailBatch(context.Context, Batch, string, string) error
	EnsureExpiryRun(context.Context, string, time.Time, string) error
	RecoverExpiryItems(context.Context, Claim) (int, error)
	ClaimExpiryItem(context.Context, Claim) (*ExpiryItem, error)
	ApplyExpiryItem(context.Context, ExpiryItem, string) error
	RequeueExpiryItem(context.Context, ExpiryItem, string, time.Duration) error
	FailExpiryItem(context.Context, ExpiryItem, string) error
}

type Directory interface {
	User(context.Context, string) (domain.DirectoryUser, error)
	Banks(context.Context) ([]domain.Route, error)
}
