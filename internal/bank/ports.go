package bank

import (
	"context"
	"encoding/json"

	domain "card-issuer-api/internal/domain"
)

type ControlStore interface {
	Transaction(context.Context) (ControlTransaction, error)
}
type RouteStore interface {
	Banks(context.Context) ([]domain.Route, error)
	HasActiveRoute(context.Context, string, string) (bool, error)
	HasActiveOrProvisioningRoute(context.Context, string, string) (bool, error)
}
type EntityStore interface {
	Entity(context.Context, string) (domain.Entity, error)
	Transaction(context.Context, string) (EntityTransaction, error)
}
type ControlTransaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ClaimIdempotency(context.Context, domain.CentralIdempotencyClaim) (domain.CentralIdempotencyReplay, error)
	FinishIdempotency(context.Context, string, string, int, json.RawMessage, string) error
	CreateBankRoute(context.Context, string, string) error
	SetBankRouteStatus(context.Context, string, string, string) error
	SetBankRouteStatusForEntity(context.Context, string, string) error
}
type EntityTransaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ProvisionEntity(context.Context, domain.EntityProvision) error
	UpdateEntity(context.Context, domain.EntityPatch) (domain.Entity, error)
}
