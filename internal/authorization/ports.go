// Package authorization owns credential-and-lifecycle verification decisions.
package authorization

import (
	"context"
	"time"

	domain "card-issuer-api/internal/domain"
)

type Store interface {
	Transaction(context.Context, string) (Transaction, error)
}

type Transaction interface {
	Commit(context.Context) error
	Rollback(context.Context) error
	LockAuthorizationReference(context.Context, string, string) error
	AuthorizationVerification(context.Context, string, string) (domain.AuthorizationVerification, bool, error)
	CardAuthorizationState(context.Context, string, string) (domain.CardAuthorizationState, bool, error)
	CreateAuthorizationVerification(context.Context, string, domain.AuthorizationVerificationCreate) (domain.AuthorizationVerification, error)
}

type CredentialVerifier interface {
	Verify(context.Context, CredentialVerification) (CredentialVerificationResult, error)
}

type CredentialVerification struct {
	PAN string
	CVV string
}

type CredentialVerificationResult struct {
	CardID string
	Valid  bool
}

type Clock func() time.Time
