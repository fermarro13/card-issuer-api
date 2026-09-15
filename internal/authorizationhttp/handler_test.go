package authorizationhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/vault"
)

type verifierStub struct {
	bank string
}

func (s *verifierStub) Verify(_ context.Context, bank, reference, pan, cvv string) (domain.AuthorizationVerification, error) {
	s.bank = bank
	return domain.AuthorizationVerification{DecisionID: "00000000-0000-4000-8000-000000000002", BankTransactionRef: reference, Approved: true}, nil
}

func TestHandlerUsesClientCertificateBankAndExcludesCredentials(t *testing.T) {
	credentialVault, err := vault.NewInMemory("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	disclosure, err := credentialVault.Provision(context.Background(), vault.ProvisionRequest{CardID: "00000000-0000-4000-8000-000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{Raw: []byte("test-client-certificate")}
	sum := sha256.Sum256(certificate.Raw)
	stub := &verifierStub{}
	handler := New(stub, map[string]string{hex.EncodeToString(sum[:]): "00000000-0000-4000-8000-000000000010"})
	body, err := json.Marshal(map[string]string{"bank_transaction_reference": "bank-reference", "pan": disclosure.PAN, "cvv": disclosure.CVV})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/authorization-verifications", nil)
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || stub.bank != "00000000-0000-4000-8000-000000000010" {
		t.Fatalf("unexpected response %d or bank %q", response.Code, stub.bank)
	}
	if strings.Contains(response.Body.String(), disclosure.PAN) || strings.Contains(response.Body.String(), disclosure.CVV) {
		t.Fatal("authorization response exposed credentials")
	}
}
