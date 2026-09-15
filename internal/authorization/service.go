package authorization

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	domain "card-issuer-api/internal/domain"
)

const decisionDeclined = "declined"

type Service struct {
	store    Store
	verifier CredentialVerifier
	now      Clock
}

func New(store Store, verifier CredentialVerifier) *Service {
	return &Service{store: store, verifier: verifier, now: time.Now}
}

// Verify returns a persisted safe decision. The caller's transaction reference
// is the replay key; credentials are deliberately neither fingerprinted nor
// persisted.
func (s *Service) Verify(ctx context.Context, bank, transactionReference, pan, cvv string) (domain.AuthorizationVerification, error) {
	if s.verifier == nil {
		return domain.AuthorizationVerification{}, errors.New("authorization: credential vault is not configured")
	}
	tx, err := s.store.Transaction(ctx, bank)
	if err != nil {
		return domain.AuthorizationVerification{}, err
	}
	defer tx.Rollback(ctx)
	if err := tx.LockAuthorizationReference(ctx, bank, transactionReference); err != nil {
		return domain.AuthorizationVerification{}, err
	}
	existing, found, err := tx.AuthorizationVerification(ctx, bank, transactionReference)
	if err != nil {
		return domain.AuthorizationVerification{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationVerification{}, err
		}
		return existing, nil
	}

	verification, err := s.verifier.Verify(ctx, CredentialVerification{PAN: pan, CVV: cvv})
	if err != nil {
		return domain.AuthorizationVerification{}, fmt.Errorf("verify credentials: %w", err)
	}
	decisionID, err := newDecisionID()
	if err != nil {
		return domain.AuthorizationVerification{}, err
	}
	input := domain.AuthorizationVerificationCreate{
		DecisionID:         decisionID,
		BankTransactionRef: transactionReference,
		Decision:           decisionDeclined,
		DecisionCode:       "credential_invalid",
		DecidedAt:          s.now().UTC(),
	}
	if verification.Valid {
		state, cardFound, err := tx.CardAuthorizationState(ctx, bank, verification.CardID)
		if err != nil {
			return domain.AuthorizationVerification{}, err
		}
		if !cardFound {
			input.DecisionCode = "card_not_found"
		} else {
			input.CardID = state.CardID
			if state.Status == "active" && (state.ExpiresAt == nil || state.ExpiresAt.After(s.now().UTC())) {
				input.Decision = "approved"
				input.DecisionCode = "approved"
			} else {
				input.DecisionCode = "card_ineligible"
			}
		}
	}
	decision, err := tx.CreateAuthorizationVerification(ctx, bank, input)
	if err != nil {
		return domain.AuthorizationVerification{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthorizationVerification{}, err
	}
	return decision, nil
}

func newDecisionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("authorization: generate decision id: %w", err)
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	v := hex.EncodeToString(b)
	return v[0:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
