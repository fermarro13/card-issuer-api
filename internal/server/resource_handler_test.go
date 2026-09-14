package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"card-issuer-api/internal/auth"
)

type handlerAuth struct{ claims auth.Claims }

func (handlerAuth) Login(context.Context, string, string, string) (auth.TokenResponse, string, time.Time, error) {
	return auth.TokenResponse{}, "", time.Time{}, errors.New("unused")
}
func (handlerAuth) Refresh(context.Context, string, string) (auth.TokenResponse, string, time.Time, error) {
	return auth.TokenResponse{}, "", time.Time{}, errors.New("unused")
}
func (handlerAuth) Logout(context.Context, string, string) error { return errors.New("unused") }
func (handlerAuth) ChangePassword(context.Context, auth.Claims, string, string, string) error {
	return errors.New("unused")
}
func (a handlerAuth) ValidateAccess(string) (auth.Claims, error) { return a.claims, nil }

type referenceHandlerService struct {
	createProduct func(context.Context, auth.Principal, string, string, []byte, string, string, string, json.RawMessage, string) (json.RawMessage, error)
	patchClient   func(context.Context, auth.Principal, string, string, string, []byte, *string, string) (json.RawMessage, error)
}

type allowBankAccess struct{}

func (allowBankAccess) CanReadBank(context.Context, auth.Principal, string) bool { return true }

func (referenceHandlerService) ListReference(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (referenceHandlerService) GetReference(context.Context, string, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (s referenceHandlerService) CreateCardProductWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, code, name, status string, configuration json.RawMessage, requestID string) (json.RawMessage, error) {
	return s.createProduct(ctx, principal, bank, key, body, code, name, status, configuration, requestID)
}
func (referenceHandlerService) CreateClientWorkflow(context.Context, auth.Principal, string, string, []byte, string, *string, string) (json.RawMessage, error) {
	return nil, nil
}
func (referenceHandlerService) CreateAccountReferenceWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (referenceHandlerService) PatchCardProductWorkflow(context.Context, auth.Principal, string, string, string, []byte, *string, *string, json.RawMessage, string) (json.RawMessage, error) {
	return nil, nil
}
func (s referenceHandlerService) PatchClientWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, display *string, requestID string) (json.RawMessage, error) {
	return s.patchClient(ctx, principal, bank, id, key, body, display, requestID)
}
func (referenceHandlerService) PatchAccountReferenceWorkflow(context.Context, auth.Principal, string, string, string, []byte, string, string) (json.RawMessage, error) {
	return nil, nil
}

func TestReferenceWriteHandlersUseTypedWorkflows(t *testing.T) {
	bank := "10000000-0000-4000-8000-000000000001"
	productID := "20000000-0000-4000-8000-000000000001"
	clientID := "30000000-0000-4000-8000-000000000001"
	service := referenceHandlerService{
		createProduct: func(_ context.Context, _ auth.Principal, gotBank, key string, _ []byte, code, name, status string, configuration json.RawMessage, _ string) (json.RawMessage, error) {
			if gotBank != bank || key != "product-key" || code != "gold" || name != "Gold" || status != "active" || string(configuration) != `{}` {
				t.Fatalf("unexpected product workflow inputs: %q %q %q %q %q %s", gotBank, key, code, name, status, configuration)
			}
			return json.RawMessage(`{"id":"` + productID + `"}`), nil
		},
		patchClient: func(_ context.Context, _ auth.Principal, gotBank, id, key string, _ []byte, display *string, _ string) (json.RawMessage, error) {
			if gotBank != bank || id != clientID || key != "client-key" || display == nil || *display != "Updated" {
				t.Fatal("unexpected client workflow inputs")
			}
			return json.RawMessage(`{"id":"` + clientID + `","display_name":"Updated"}`), nil
		},
	}
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: "operator", UserSummary: auth.UserSummary{Role: "issuer_operator", Status: "enabled"}}}, Dependencies{Access: allowBankAccess{}, Catalog: service}, []byte("01234567890123456789012345678901"))
	for _, tc := range []struct {
		method, path, key, body string
		want                    int
	}{
		{http.MethodPost, "/v1/banks/" + bank + "/card-products", "product-key", `{"product_code":"gold","name":"Gold","configuration":{}}`, http.StatusCreated},
		{http.MethodPatch, "/v1/banks/" + bank + "/clients/" + clientID, "client-key", `{"display_name":"Updated"}`, http.StatusOK},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer token")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", tc.key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestReferenceWriteRejectsNonIssuerOperator(t *testing.T) {
	bank := "10000000-0000-4000-8000-000000000001"
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: "reader", UserSummary: auth.UserSummary{Role: "issuer_readonly", Status: "enabled"}}}, Dependencies{Access: allowBankAccess{}, Catalog: referenceHandlerService{}}, nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/banks/"+bank+"/clients", strings.NewReader(`{"external_client_ref":"client"}`))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
