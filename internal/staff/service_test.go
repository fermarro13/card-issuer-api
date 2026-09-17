package staff

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"

	"card-issuer-api/internal/auth"
	"card-issuer-api/internal/domain"
	"card-issuer-api/internal/idempotency"
)

func TestPasswordBearingWorkflowsClaimKeyedFingerprints(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	body := []byte(`{"username":"new-user","password":"Sensitive-Password-9!"}`)
	stop := errors.New("stop after idempotency claim")
	principal := auth.Principal{UserID: "actor", Role: "issuer_operator"}

	for _, tc := range []struct {
		name   string
		invoke func(*Service) error
	}{
		{
			name: "user creation",
			invoke: func(service *Service) error {
				_, _, err := service.CreateUserWorkflow(context.Background(), principal, "create-key", body, "new-user", "Sensitive-Password-9!", "issuer_readonly", "", "request")
				return err
			},
		},
		{
			name: "password reset",
			invoke: func(service *Service) error {
				_, _, err := service.SetUserPasswordWorkflow(context.Background(), principal, "subject", "reset-key", body, "Sensitive-Password-9!", "request")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fingerprinter, err := idempotency.NewFingerprinter(key)
			if err != nil {
				t.Fatal(err)
			}
			tx := &claimCaptureTransaction{err: stop}
			service := New(&claimCaptureStore{tx: tx}, nil, "shard", fingerprinter)
			if err := tc.invoke(service); !errors.Is(err, stop) {
				t.Fatalf("workflow error = %v, want claim sentinel", err)
			}

			mac := hmac.New(sha256.New, key)
			_, _ = mac.Write(body)
			if !bytes.Equal(tx.claim.Fingerprint, mac.Sum(nil)) {
				t.Fatal("idempotency claim did not receive the expected HMAC-SHA-256 fingerprint")
			}
			plain := sha256.Sum256(body)
			if bytes.Equal(tx.claim.Fingerprint, plain[:]) {
				t.Fatal("idempotency claim received plain SHA-256 of password-bearing JSON")
			}
		})
	}
}

type claimCaptureStore struct {
	DirectoryStore
	tx Transaction
}

func (s *claimCaptureStore) Transaction(context.Context) (Transaction, error) {
	return s.tx, nil
}

type claimCaptureTransaction struct {
	Transaction
	claim domain.CentralIdempotencyClaim
	err   error
}

func (tx *claimCaptureTransaction) Rollback(context.Context) error {
	return nil
}

func (tx *claimCaptureTransaction) ClaimIdempotency(_ context.Context, claim domain.CentralIdempotencyClaim) (domain.CentralIdempotencyReplay, error) {
	tx.claim = claim
	return domain.CentralIdempotencyReplay{}, tx.err
}
