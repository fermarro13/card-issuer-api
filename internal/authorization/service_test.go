package authorization

import (
	"context"
	"testing"
	"time"

	domain "card-issuer-api/internal/domain"
)

type storeStub struct{ tx *transactionStub }

func (s storeStub) Transaction(context.Context, string) (Transaction, error) { return s.tx, nil }

type transactionStub struct {
	existing       *domain.AuthorizationVerification
	card           domain.CardAuthorizationState
	cardFound      bool
	created        domain.AuthorizationVerificationCreate
	verificationID string
}

func (t *transactionStub) Commit(context.Context) error   { return nil }
func (t *transactionStub) Rollback(context.Context) error { return nil }
func (t *transactionStub) LockAuthorizationReference(context.Context, string, string) error {
	return nil
}
func (t *transactionStub) AuthorizationVerification(context.Context, string, string) (domain.AuthorizationVerification, bool, error) {
	if t.existing == nil {
		return domain.AuthorizationVerification{}, false, nil
	}
	return *t.existing, true, nil
}
func (t *transactionStub) CardAuthorizationState(context.Context, string, string) (domain.CardAuthorizationState, bool, error) {
	return t.card, t.cardFound, nil
}
func (t *transactionStub) CreateAuthorizationVerification(_ context.Context, _ string, input domain.AuthorizationVerificationCreate) (domain.AuthorizationVerification, error) {
	t.created = input
	return domain.AuthorizationVerification{DecisionID: input.DecisionID, BankTransactionRef: input.BankTransactionRef, Approved: input.Decision == "approved"}, nil
}

type verifierStub struct {
	result CredentialVerificationResult
	calls  int
}

func (v *verifierStub) Verify(context.Context, CredentialVerification) (CredentialVerificationResult, error) {
	v.calls++
	return v.result, nil
}

func TestServiceVerifyApprovesOnlyActiveUnexpiredCard(t *testing.T) {
	tx := &transactionStub{card: domain.CardAuthorizationState{CardID: "00000000-0000-4000-8000-000000000001", Status: "active"}, cardFound: true}
	verifier := &verifierStub{result: CredentialVerificationResult{CardID: tx.card.CardID, Valid: true}}
	service := New(storeStub{tx: tx}, verifier)
	service.now = func() time.Time { return time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC) }
	decision, err := service.Verify(context.Background(), "00000000-0000-4000-8000-000000000010", "reference", "", "")
	if err != nil || !decision.Approved || tx.created.DecisionCode != "approved" || tx.created.CardID != tx.card.CardID {
		t.Fatalf("Verify() = %#v, %v; stored %#v", decision, err, tx.created)
	}
}

func TestServiceVerifyDeclinesInactiveOrExpiredCard(t *testing.T) {
	tests := []struct {
		name  string
		state domain.CardAuthorizationState
		now   time.Time
	}{
		{name: "suspended", state: domain.CardAuthorizationState{CardID: "00000000-0000-4000-8000-000000000001", Status: "suspended"}, now: time.Now().UTC()},
		{name: "expired", state: domain.CardAuthorizationState{CardID: "00000000-0000-4000-8000-000000000001", Status: "active", ExpiresAt: timePtr(time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC))}, now: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &transactionStub{card: test.state, cardFound: true}
			service := New(storeStub{tx: tx}, &verifierStub{result: CredentialVerificationResult{CardID: test.state.CardID, Valid: true}})
			service.now = func() time.Time { return test.now }
			decision, err := service.Verify(context.Background(), "bank", "reference", "", "")
			if err != nil || decision.Approved || tx.created.DecisionCode != "card_ineligible" {
				t.Fatalf("Verify() = %#v, %v; stored %#v", decision, err, tx.created)
			}
		})
	}
}

func TestServiceVerifyReplaysWithoutRevalidation(t *testing.T) {
	existing := &domain.AuthorizationVerification{DecisionID: "00000000-0000-4000-8000-000000000002", BankTransactionRef: "reference", Approved: false}
	verifier := &verifierStub{}
	service := New(storeStub{tx: &transactionStub{existing: existing}}, verifier)
	decision, err := service.Verify(context.Background(), "bank", "reference", "", "")
	if err != nil || decision != *existing || verifier.calls != 0 {
		t.Fatalf("Verify() = %#v, %v; verifier calls = %d", decision, err, verifier.calls)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
