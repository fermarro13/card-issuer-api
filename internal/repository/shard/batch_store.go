package shardrepository

import (
	"context"

	"card-issuer-api/internal/batch"
	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BatchStore implements batch's persistence port.
type BatchStore struct{ reader *Reader }

func NewBatch(pool *pgxpool.Pool) *BatchStore { return &BatchStore{reader: NewReader(pool)} }

func (s *BatchStore) CardStatusBatches(ctx context.Context, bankID string) ([]domain.CardStatusBatch, error) {
	return s.reader.CardStatusBatches(ctx, bankID)
}
func (s *BatchStore) CardStatusBatch(ctx context.Context, bankID, id string) (domain.CardStatusBatch, error) {
	return s.reader.CardStatusBatch(ctx, bankID, id)
}
func (s *BatchStore) CardStatusBatchItems(ctx context.Context, bankID, batchID string) ([]domain.CardStatusBatchItem, error) {
	return s.reader.CardStatusBatchItems(ctx, bankID, batchID)
}
func (s *BatchStore) Transaction(ctx context.Context, bankID string) (batch.Transaction, error) {
	tx, err := s.reader.transaction(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
