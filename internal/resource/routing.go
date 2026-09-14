package resource

import (
	"context"
	"time"
)

// RoutingStore is the read-only routing boundary used by the resource API.
// Its implementation owns the control-database query details.
type RoutingStore interface {
	Banks(context.Context) ([]Route, error)
	HasActiveRoute(context.Context, string, string) (bool, error)
	HasActiveOrProvisioningRoute(context.Context, string, string) (bool, error)
}

type Route struct {
	EntityID        string
	ShardID         string
	PlacementStatus string
	CreatedAt       time.Time
}
