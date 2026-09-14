package routingrepository

import (
	"context"
	"fmt"

	"card-issuer-api/internal/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Banks(ctx context.Context) ([]resource.Route, error) {
	rows, err := s.pool.Query(ctx, "SELECT entity_id::text,shard_id,placement_status,created_at FROM control.bank_routing_entries ORDER BY created_at DESC,entity_id DESC")
	if err != nil {
		return nil, fmt.Errorf("list bank routes: %w", err)
	}
	defer rows.Close()
	routes := make([]resource.Route, 0)
	for rows.Next() {
		var route resource.Route
		if err := rows.Scan(&route.EntityID, &route.ShardID, &route.PlacementStatus, &route.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bank route: %w", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bank routes: %w", err)
	}
	return routes, nil
}

func (s *Store) HasActiveRoute(ctx context.Context, entityID, shardID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries WHERE entity_id=$1 AND shard_id=$2 AND placement_status='active'", entityID, shardID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check active bank route: %w", err)
	}
	return count == 1, nil
}

func (s *Store) HasActiveOrProvisioningRoute(ctx context.Context, entityID, shardID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries WHERE entity_id=$1 AND shard_id=$2 AND placement_status IN ('active','paused','provisioning')", entityID, shardID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check visible bank route: %w", err)
	}
	return count == 1, nil
}
