package resource

import (
	"context"
	"time"
)

type ShardReader interface {
	Card(context.Context, string, string) (Card, error)
	Cards(context.Context, string, CardFilter) ([]Card, error)
}

type CardFilter struct {
	ClientID  string
	AccountID string
	ProductID string
	Status    string
}

// Card is the persistence-neutral representation returned by the shard reader.
// JSON tags preserve the existing public representation at the HTTP boundary.
type Card struct {
	ID                 string     `json:"id"`
	ClientID           string     `json:"client_id"`
	AccountReferenceID string     `json:"account_reference_id"`
	ProductID          string     `json:"product_id"`
	Status             string     `json:"status"`
	PredecessorCardID  *string    `json:"predecessor_card_id"`
	IssuedAt           *time.Time `json:"issued_at"`
	ActivatedAt        *time.Time `json:"activated_at"`
	SuspendedAt        *time.Time `json:"suspended_at"`
	ClosedAt           *time.Time `json:"closed_at"`
	ExpiresAt          *time.Time `json:"expires_at"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}
