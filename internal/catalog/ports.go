package catalog

import (
	"context"
	"encoding/json"

	domain "card-issuer-api/internal/domain"
)

type Store interface {
	CardProducts(context.Context, string) ([]domain.CardProduct, error)
	CardProduct(context.Context, string, string) (domain.CardProduct, error)
	Clients(context.Context, string) ([]domain.Client, error)
	Client(context.Context, string, string) (domain.Client, error)
	AccountReferences(context.Context, string) ([]domain.AccountReference, error)
	AccountReference(context.Context, string, string) (domain.AccountReference, error)
	Transaction(context.Context, string) (Transaction, error)
}
type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ClaimIdempotency(context.Context, domain.ShardIdempotencyClaim) (domain.ShardIdempotencyReplay, error)
	FinishIdempotency(context.Context, string, string, string, int, json.RawMessage, string, string) error
	CreateCardProduct(context.Context, string, domain.CardProductCreate) (domain.CardProduct, error)
	UpdateCardProduct(context.Context, string, string, domain.CardProductPatch) (domain.CardProduct, bool, error)
	CreateClient(context.Context, string, domain.ClientCreate) (domain.Client, error)
	UpdateClient(context.Context, string, string, domain.ClientPatch) (domain.Client, bool, error)
	CreateAccountReference(context.Context, string, domain.AccountReferenceCreate) (domain.AccountReference, error)
	UpdateAccountReference(context.Context, string, string, domain.AccountReferencePatch) (domain.AccountReference, bool, error)
	RecordReferenceAudit(context.Context, domain.ReferenceAudit) error
}
