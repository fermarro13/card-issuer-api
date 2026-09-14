package shardrepository

import (
	"context"

	"card-issuer-api/internal/card"
	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CardStore implements card's persistence port.
type CardStore struct{ reader *Reader }

func NewCard(pool *pgxpool.Pool) *CardStore { return &CardStore{reader: NewReader(pool)} }

func (s *CardStore) Card(ctx context.Context, bankID, id string) (domain.Card, error) {
	return s.reader.Card(ctx, bankID, id)
}
func (s *CardStore) Cards(ctx context.Context, bankID string, filter domain.CardFilter) ([]domain.Card, error) {
	return s.reader.Cards(ctx, bankID, filter)
}
func (s *CardStore) CardOperations(ctx context.Context, bankID, cardID string) ([]domain.CardOperation, error) {
	return s.reader.CardOperations(ctx, bankID, cardID)
}
func (s *CardStore) CardStatusHistory(ctx context.Context, bankID, cardID string) ([]domain.CardStatusHistory, error) {
	return s.reader.CardStatusHistory(ctx, bankID, cardID)
}
func (s *CardStore) Transaction(ctx context.Context, bankID string) (card.Transaction, error) {
	tx, err := s.reader.transaction(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
