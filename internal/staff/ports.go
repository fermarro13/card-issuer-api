package staff

import (
	"context"
	"encoding/json"

	domain "card-issuer-api/internal/domain"
)

type DirectoryStore interface {
	Users(context.Context) ([]domain.DirectoryUser, error)
	User(context.Context, string) (domain.DirectoryUser, error)
	Transaction(context.Context) (Transaction, error)
}
type RouteStore interface {
	HasActiveRoute(context.Context, string, string) (bool, error)
	HasActiveOrProvisioningRoute(context.Context, string, string) (bool, error)
}
type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	ClaimIdempotency(context.Context, domain.CentralIdempotencyClaim) (domain.CentralIdempotencyReplay, error)
	FinishIdempotency(context.Context, string, string, int, json.RawMessage, string) error
	CreateUser(context.Context, domain.StaffUserCreate) (domain.DirectoryUser, error)
	UpdateUserRole(context.Context, string, string, string, string) (domain.DirectoryUser, bool, error)
	UpdateUserStatus(context.Context, string, string, string) (domain.DirectoryUser, bool, error)
	UpdateUserPassword(context.Context, string, string, string) (domain.DirectoryUser, bool, error)
	RecordAuthenticationAudit(context.Context, domain.AuthenticationAudit) error
}
