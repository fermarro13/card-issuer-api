package shardrepository

import (
	"context"
	"fmt"

	"card-issuer-api/internal/resource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Reader struct{ pool *pgxpool.Pool }

func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

func (r *Reader) Card(ctx context.Context, bank, id string) (resource.Card, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return resource.Card{}, err
	}
	defer tx.Rollback(ctx)
	card, err := scanCard(tx.QueryRow(ctx, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status::text,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND id=$2", bank, id))
	if err != nil {
		return resource.Card{}, fmt.Errorf("get card: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return resource.Card{}, fmt.Errorf("commit card read: %w", err)
	}
	return card, nil
}

func (r *Reader) Cards(ctx context.Context, bank string, filter resource.CardFilter) ([]resource.Card, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status::text,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND ($2::uuid IS NULL OR client_id=$2) AND ($3::uuid IS NULL OR account_reference_id=$3) AND ($4::uuid IS NULL OR product_id=$4) AND ($5::bank.card_state IS NULL OR status=$5) ORDER BY created_at DESC,id DESC", bank, nullable(filter.ClientID), nullable(filter.AccountID), nullable(filter.ProductID), nullable(filter.Status))
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	defer rows.Close()
	cards := make([]resource.Card, 0)
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cards: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card list: %w", err)
	}
	return cards, nil
}

type cardRow interface{ Scan(...any) error }

func scanCard(row cardRow) (resource.Card, error) {
	var card resource.Card
	err := row.Scan(
		&card.ID, &card.ClientID, &card.AccountReferenceID, &card.ProductID, &card.Status,
		&card.PredecessorCardID, &card.IssuedAt, &card.ActivatedAt, &card.SuspendedAt,
		&card.ClosedAt, &card.ExpiresAt, &card.Version, &card.CreatedAt, &card.UpdatedAt,
	)
	return card, err
}

func (r *Reader) beginTenant(ctx context.Context, bank string) (pgx.Tx, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin shard transaction: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.entity_id',$1,true)", bank); err != nil {
		tx.Rollback(ctx)
		return nil, fmt.Errorf("set shard tenant context: %w", err)
	}
	return tx, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
