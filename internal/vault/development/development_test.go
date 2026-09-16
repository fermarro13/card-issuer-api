package development

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"card-issuer-api/internal/vault"
)

func TestClientProvisionVerifyAndRevoke(t *testing.T) {
	credentialVault, err := vault.NewInMemory("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(credentialVault))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	disclosure, err := client.Provision(context.Background(), vault.ProvisionRequest{CardID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := client.Verify(context.Background(), vault.VerificationRequest{PAN: disclosure.PAN, CVV: disclosure.CVV})
	if err != nil || !verified.Valid {
		t.Fatalf("Verify() valid = %t, error = %v", verified.Valid, err)
	}
	if err := client.Revoke(context.Background(), verified.CardID); err != nil {
		t.Fatal(err)
	}
	verified, err = client.Verify(context.Background(), vault.VerificationRequest{PAN: disclosure.PAN, CVV: disclosure.CVV})
	if err != nil || verified.Valid {
		t.Fatalf("revoked Verify() valid = %t, error = %v", verified.Valid, err)
	}
}

func TestHandlerRejectsMalformedInputAndUsesNoStore(t *testing.T) {
	credentialVault, err := vault.NewInMemory("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(credentialVault))
	defer server.Close()
	response, err := http.Post(server.URL+"/v1/verify", "application/json", strings.NewReader(`{"pan":"1234","extra":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status = %d, Cache-Control = %q", response.StatusCode, response.Header.Get("Cache-Control"))
	}
}

func TestClientUnavailableAndCancellationAreSafe(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pan := "4" + strings.Repeat("0", 15)
	cvv := strings.Repeat("1", 3)
	_, err = client.Verify(ctx, vault.VerificationRequest{PAN: pan, CVV: cvv})
	if !errors.Is(err, errUnavailable) || strings.Contains(err.Error(), pan) || strings.Contains(err.Error(), cvv) {
		t.Fatalf("safe unavailable error = %v", err)
	}
}

func TestClientHonorsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.Verify(ctx, vault.VerificationRequest{PAN: "4" + strings.Repeat("0", 15), CVV: strings.Repeat("1", 3)})
	if !errors.Is(err, errUnavailable) {
		t.Fatalf("deadline error = %v", err)
	}
}

func TestClientRejectsMalformedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":false}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Verify(context.Background(), vault.VerificationRequest{PAN: "4" + strings.Repeat("0", 15), CVV: strings.Repeat("1", 3)})
	if !errors.Is(err, errUnavailable) {
		t.Fatalf("malformed response error = %v", err)
	}
}
