package shardrepository

import (
	"context"

	"card-issuer-api/internal/catalog"
	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CatalogStore implements catalog's reference-data persistence port.
type CatalogStore struct{ reader *Reader }

func NewCatalog(pool *pgxpool.Pool) *CatalogStore { return &CatalogStore{reader: NewReader(pool)} }

func (s *CatalogStore) CardProducts(ctx context.Context, bankID string) ([]domain.CardProduct, error) {
	return s.reader.CardProducts(ctx, bankID)
}
func (s *CatalogStore) CardProduct(ctx context.Context, bankID, id string) (domain.CardProduct, error) {
	return s.reader.CardProduct(ctx, bankID, id)
}
func (s *CatalogStore) Clients(ctx context.Context, bankID string) ([]domain.Client, error) {
	return s.reader.Clients(ctx, bankID)
}
func (s *CatalogStore) Client(ctx context.Context, bankID, id string) (domain.Client, error) {
	return s.reader.Client(ctx, bankID, id)
}
func (s *CatalogStore) AccountReferences(ctx context.Context, bankID string) ([]domain.AccountReference, error) {
	return s.reader.AccountReferences(ctx, bankID)
}
func (s *CatalogStore) AccountReference(ctx context.Context, bankID, id string) (domain.AccountReference, error) {
	return s.reader.AccountReference(ctx, bankID, id)
}
func (s *CatalogStore) Transaction(ctx context.Context, bankID string) (catalog.Transaction, error) {
	tx, err := s.reader.transaction(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
