package controlrepository

import (
	"context"

	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/staff"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StaffStore implements staff's central-control persistence port.
type StaffStore struct{ store *Store }

func NewStaff(pool *pgxpool.Pool) *StaffStore { return &StaffStore{store: New(pool)} }

func (s *StaffStore) Users(ctx context.Context) ([]domain.DirectoryUser, error) {
	return s.store.Users(ctx)
}
func (s *StaffStore) User(ctx context.Context, id string) (domain.DirectoryUser, error) {
	return s.store.User(ctx, id)
}
func (s *StaffStore) Transaction(ctx context.Context) (staff.Transaction, error) {
	tx, err := s.store.transaction(ctx)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
