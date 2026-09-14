package controlrepository

import (
	"context"
	"fmt"

	"card-issuer-api/internal/resource"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Users(ctx context.Context) ([]resource.DirectoryUser, error) {
	rows, err := s.pool.Query(ctx, "SELECT id::text,normalized_username,role,COALESCE(entity_id::text,''),status,created_at,updated_at FROM control.users ORDER BY created_at DESC,id DESC")
	if err != nil {
		return nil, fmt.Errorf("list staff users: %w", err)
	}
	defer rows.Close()
	users := make([]resource.DirectoryUser, 0)
	for rows.Next() {
		var user resource.DirectoryUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.EntityID, &user.Status, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan staff user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staff users: %w", err)
	}
	return users, nil
}
