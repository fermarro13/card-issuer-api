package batch

import (
	"context"
	"encoding/json"

	domain "card-issuer-api/internal/domain"
)

type Store interface {
	CardStatusBatches(context.Context, string) ([]domain.CardStatusBatch, error)
	CardStatusBatch(context.Context, string, string) (domain.CardStatusBatch, error)
	CardStatusBatchItems(context.Context, string, string) ([]domain.CardStatusBatchItem, error)
	Transaction(context.Context, string) (Transaction, error)
}
type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ClaimIdempotency(context.Context, domain.ShardIdempotencyClaim) (domain.ShardIdempotencyReplay, error)
	FinishIdempotency(context.Context, string, string, string, int, json.RawMessage, string, string) error
	CardsExist(context.Context, string, []string) (bool, error)
	CreateCardStatusBatchDraft(context.Context, string, domain.CardStatusBatchDraft) (domain.CardStatusBatch, []domain.CardStatusBatchItem, error)
	LockCardStatusBatch(context.Context, string, string) (domain.CardStatusBatch, error)
	ExecuteCardStatusBatch(context.Context, string, string, string, string, string, string) (domain.CardStatusBatch, error)
	CancelCardStatusBatch(context.Context, string, string, string, string, string, string) (domain.CardStatusBatch, error)
	RetryCardStatusBatch(context.Context, string, string, domain.CardStatusBatchRetry) (domain.CardStatusBatch, []domain.CardStatusBatchItem, error)
}
