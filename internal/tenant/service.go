// Package tenant owns trusted bank-route visibility and bank-scoped access
// decisions shared by the business application services.
package tenant

import (
	"context"

	"card-issuer-api/internal/auth"
)

// RouteStore is the small routing read port needed to validate the local
// shard's view of a bank. PostgreSQL details remain in repository/routing.
type RouteStore interface {
	HasActiveRoute(context.Context, string, string) (bool, error)
	HasActiveOrProvisioningRoute(context.Context, string, string) (bool, error)
}

type Service struct {
	routes  RouteStore
	shardID string
}

func New(routes RouteStore, shardID string) *Service {
	return &Service{routes: routes, shardID: shardID}
}

func (s *Service) Active(ctx context.Context, bankID string) bool {
	ok, err := s.routes.HasActiveRoute(ctx, bankID, s.shardID)
	return err == nil && ok
}

func (s *Service) ActiveOrProvisioning(ctx context.Context, bankID string) bool {
	ok, err := s.routes.HasActiveOrProvisioningRoute(ctx, bankID, s.shardID)
	return err == nil && ok
}

func (s *Service) CanRead(ctx context.Context, principal auth.Principal, bankID string) bool {
	if principal.Role == "issuer_operator" || principal.Role == "issuer_readonly" {
		return s.Active(ctx, bankID)
	}
	return principal.EntityID == bankID && s.Active(ctx, bankID)
}

// CanReadBank is the HTTP-facing bank-access boundary.
func (s *Service) CanReadBank(ctx context.Context, principal auth.Principal, bankID string) bool {
	return s.CanRead(ctx, principal, bankID)
}
