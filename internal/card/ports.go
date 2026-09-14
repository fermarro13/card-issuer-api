package card

import (
	"context"
	"encoding/json"

	domain "card-issuer-api/internal/domain"
)

type Store interface {
	Card(context.Context, string, string) (domain.Card, error)
	Cards(context.Context, string, domain.CardFilter) ([]domain.Card, error)
	CardOperations(context.Context, string, string) ([]domain.CardOperation, error)
	CardStatusHistory(context.Context, string, string) ([]domain.CardStatusHistory, error)
	Transaction(context.Context, string) (Transaction, error)
}
type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ClaimIdempotency(context.Context, domain.ShardIdempotencyClaim) (domain.ShardIdempotencyReplay, error)
	FinishIdempotency(context.Context, string, string, string, int, json.RawMessage, string, string) error
	IssueReferencesValid(context.Context, string, string, string, string) (bool, error)
	IssueCard(context.Context, string, domain.CardIssue) (domain.Card, domain.CardOperation, error)
	LockCard(context.Context, string, string) (domain.CardState, error)
	ReplacementPending(context.Context, string, string) (bool, error)
	ReplaceCard(context.Context, string, domain.CardReplacement) (domain.Card, domain.CardOperation, error)
	ApplyCardCommand(context.Context, string, domain.CardCommand) (domain.Card, domain.CardOperation, error)
	RetryExpiryItem(context.Context, string, string, string, string) (bool, error)
	RecordExpiryRetryAudit(context.Context, string, string, string, string) error
}
