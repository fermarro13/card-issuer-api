package controlrepository

import (
	"context"

	"card-issuer-api/internal/bank"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BankStore implements bank's central-control persistence port.
type BankStore struct{ store *Store }

func NewBank(pool *pgxpool.Pool) *BankStore { return &BankStore{store: New(pool)} }

func (s *BankStore) Transaction(ctx context.Context) (bank.ControlTransaction, error) {
	tx, err := s.store.transaction(ctx)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
