package vault

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewInMemoryRejectsProduction(t *testing.T) {
	_, err := NewInMemory("production", make([]byte, 32))
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("NewInMemory() error = %v, want ErrNotAllowed", err)
	}
}

func TestInMemoryProvisionVerifyAndRevoke(t *testing.T) {
	credentialVault, err := NewInMemory("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	cardID := "00000000-0000-4000-8000-000000000001"
	disclosure, err := credentialVault.Provision(context.Background(), ProvisionRequest{CardID: cardID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(disclosure.MaskedPAN, "******") || disclosure.MaskedPAN == disclosure.PAN {
		t.Fatal("provision did not return a masked display")
	}
	for token := range credentialVault.cards {
		if strings.Contains(token, disclosure.PAN) || strings.Contains(token, disclosure.CVV) {
			t.Fatal("in-memory vault retained a raw credential")
		}
	}
	result, err := credentialVault.Verify(context.Background(), VerificationRequest{PAN: disclosure.PAN, CVV: disclosure.CVV})
	if err != nil || !result.Valid || result.CardID != cardID {
		t.Fatalf("Verify() = %#v, %v", result, err)
	}
	if err := credentialVault.Revoke(context.Background(), cardID); err != nil {
		t.Fatal(err)
	}
	result, err = credentialVault.Verify(context.Background(), VerificationRequest{PAN: disclosure.PAN, CVV: disclosure.CVV})
	if err != nil || result.Valid {
		t.Fatalf("revoked Verify() = %#v, %v", result, err)
	}
}
