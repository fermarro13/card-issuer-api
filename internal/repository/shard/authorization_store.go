package shardrepository

import (
	"context"
	"errors"

	"card-issuer-api/internal/authorization"
	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuthorizationStore struct{ reader *Reader }

func NewAuthorization(pool *pgxpool.Pool) *AuthorizationStore {
	return &AuthorizationStore{reader: NewReader(pool)}
}

func (s *AuthorizationStore) Transaction(ctx context.Context, bank string) (authorization.Transaction, error) {
	tx, err := s.reader.transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (t transaction) LockAuthorizationReference(ctx context.Context, bank, reference string) error {
	_, err := t.tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))", bank, reference)
	return err
}

func (t transaction) AuthorizationVerification(ctx context.Context, bank, reference string) (domain.AuthorizationVerification, bool, error) {
	var value domain.AuthorizationVerification
	var decision string
	// LockAuthorizationReference already serializes this bank/reference pair. A
	// row-level UPDATE lock would require an unnecessary UPDATE grant.
	err := t.tx.QueryRow(ctx, "SELECT decision_id::text,bank_transaction_reference,decision FROM bank.authorization_verifications WHERE entity_id=$1 AND bank_transaction_reference=$2", bank, reference).Scan(&value.DecisionID, &value.BankTransactionRef, &decision)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AuthorizationVerification{}, false, nil
	}
	if err != nil {
		return domain.AuthorizationVerification{}, false, err
	}
	value.Approved = decision == "approved"
	return value, true, nil
}

func (t transaction) CardAuthorizationState(ctx context.Context, bank, cardID string) (domain.CardAuthorizationState, bool, error) {
	var value domain.CardAuthorizationState
	err := t.tx.QueryRow(ctx, "SELECT id::text,status::text,expires_at FROM bank.cards WHERE entity_id=$1 AND id=$2", bank, cardID).Scan(&value.CardID, &value.Status, &value.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CardAuthorizationState{}, false, nil
	}
	if err != nil {
		return domain.CardAuthorizationState{}, false, err
	}
	return value, true, nil
}

func (t transaction) CreateAuthorizationVerification(ctx context.Context, bank string, input domain.AuthorizationVerificationCreate) (domain.AuthorizationVerification, error) {
	var cardID *string
	if input.CardID != "" {
		cardID = &input.CardID
	}
	var result domain.AuthorizationVerification
	var decision string
	err := t.tx.QueryRow(ctx, "INSERT INTO bank.authorization_verifications(entity_id,decision_id,bank_transaction_reference,card_id,decision,decision_code,decided_at) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING decision_id::text,bank_transaction_reference,decision", bank, input.DecisionID, input.BankTransactionRef, cardID, input.Decision, input.DecisionCode, input.DecidedAt).Scan(&result.DecisionID, &result.BankTransactionRef, &decision)
	if err != nil {
		return domain.AuthorizationVerification{}, err
	}
	result.Approved = decision == "approved"
	return result, nil
}

var _ authorization.Store = (*AuthorizationStore)(nil)
