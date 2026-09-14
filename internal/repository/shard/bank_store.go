package shardrepository

import (
	"context"

	"card-issuer-api/internal/bank"
	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BankStore implements bank's entity persistence port.
type BankStore struct{ reader *Reader }

func NewBank(pool *pgxpool.Pool) *BankStore { return &BankStore{reader: NewReader(pool)} }

func (s *BankStore) Entity(ctx context.Context, id string) (domain.Entity, error) {
	return s.reader.Entity(ctx, id)
}

func (s *BankStore) Transaction(ctx context.Context, bankID string) (bank.EntityTransaction, error) {
	tx, err := s.reader.transaction(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
