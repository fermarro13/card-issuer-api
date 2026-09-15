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
	createClient  func(context.Context, auth.Principal, string, string, []byte, string, *string, string) (json.RawMessage, error)
	createAccount func(context.Context, auth.Principal, string, string, []byte, string, string, string) (json.RawMessage, error)
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
func (s referenceHandlerService) CreateClientWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, external string, display *string, requestID string) (json.RawMessage, error) {
	if s.createClient == nil {
		return nil, errors.New("unexpected client workflow")
	}
	return s.createClient(ctx, principal, bank, key, body, external, display, requestID)
}
func (s referenceHandlerService) CreateAccountReferenceWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, clientID, external, requestID string) (json.RawMessage, error) {
	if s.createAccount == nil {
		return nil, errors.New("unexpected account workflow")
	}
	return s.createAccount(ctx, principal, bank, key, body, clientID, external, requestID)
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

type assignedBankAccess struct{ bank string }

func (a assignedBankAccess) CanReadBank(_ context.Context, principal auth.Principal, bank string) bool {
	return bank == a.bank && principal.EntityID == a.bank
}

type cardHandlerService struct {
	issue   func(context.Context, auth.Principal, string, string, []byte, string, string, string, string, string) (json.RawMessage, error)
	command func(context.Context, auth.Principal, string, string, string, string, []byte, string, string) (json.RawMessage, int, error)
}

func (cardHandlerService) ListCards(context.Context, string, string, string, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (cardHandlerService) GetCard(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (cardHandlerService) ListCardOperations(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (cardHandlerService) ListCardStatusHistory(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (s cardHandlerService) IssueCardWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, clientID, accountID, productID, reason, requestID string) (json.RawMessage, error) {
	return s.issue(ctx, principal, bank, key, body, clientID, accountID, productID, reason, requestID)
}
func (s cardHandlerService) CardCommandWorkflow(ctx context.Context, principal auth.Principal, bank, id, action, key string, body []byte, reason, requestID string) (json.RawMessage, int, error) {
	return s.command(ctx, principal, bank, id, action, key, body, reason, requestID)
}
func (cardHandlerService) RetryExpiryWorkflow(context.Context, auth.Principal, string, string, string, string, []byte, string, string) error {
	return nil
}

func TestBankOperatorCanCreateAndReplaceWithinAssignedBank(t *testing.T) {
	const (
		bank      = "10000000-0000-4000-8000-000000000001"
		otherBank = "10000000-0000-4000-8000-000000000002"
		clientID  = "20000000-0000-4000-8000-000000000001"
		accountID = "30000000-0000-4000-8000-000000000001"
		productID = "40000000-0000-4000-8000-000000000001"
		cardID    = "50000000-0000-4000-8000-000000000001"
	)
	principal := auth.Principal{UserID: "operator", Role: "bank_operator", EntityID: bank}
	assertPrincipal := func(got auth.Principal) {
		if got != principal {
			t.Fatalf("workflow principal = %#v; want %#v", got, principal)
		}
	}
	catalog := referenceHandlerService{
		createProduct: func(_ context.Context, got auth.Principal, gotBank, _ string, _ []byte, _ string, _ string, _ string, _ json.RawMessage, _ string) (json.RawMessage, error) {
			assertPrincipal(got)
			if gotBank != bank {
				t.Fatalf("product bank = %q; want %q", gotBank, bank)
			}
			return json.RawMessage(`{"id":"` + productID + `"}`), nil
		},
		createClient: func(_ context.Context, got auth.Principal, gotBank, _ string, _ []byte, _ string, _ *string, _ string) (json.RawMessage, error) {
			assertPrincipal(got)
			if gotBank != bank {
				t.Fatalf("client bank = %q; want %q", gotBank, bank)
			}
			return json.RawMessage(`{"id":"` + clientID + `"}`), nil
		},
		createAccount: func(_ context.Context, got auth.Principal, gotBank, _ string, _ []byte, gotClientID, _ string, _ string) (json.RawMessage, error) {
			assertPrincipal(got)
			if gotBank != bank || gotClientID != clientID {
				t.Fatalf("account workflow received bank=%q client=%q", gotBank, gotClientID)
			}
			return json.RawMessage(`{"id":"` + accountID + `"}`), nil
		},
	}
	card := cardHandlerService{
		issue: func(_ context.Context, got auth.Principal, gotBank, _ string, _ []byte, gotClientID, gotAccountID, gotProductID, _ string, _ string) (json.RawMessage, error) {
			assertPrincipal(got)
			if gotBank != bank || gotClientID != clientID || gotAccountID != accountID || gotProductID != productID {
				t.Fatalf("issue workflow received bank=%q client=%q account=%q product=%q", gotBank, gotClientID, gotAccountID, gotProductID)
			}
			return json.RawMessage(`{"card":{"id":"` + cardID + `"}}`), nil
		},
		command: func(_ context.Context, got auth.Principal, gotBank, gotCardID, action, _ string, _ []byte, _ string, _ string) (json.RawMessage, int, error) {
			assertPrincipal(got)
			if gotBank != bank || gotCardID != cardID || action != "replace" {
				t.Fatalf("command workflow received bank=%q card=%q action=%q", gotBank, gotCardID, action)
			}
			return json.RawMessage(`{"card":{"id":"` + cardID + `"}}`), http.StatusCreated, nil
		},
	}
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: principal.UserID, EntityID: bank, UserSummary: auth.UserSummary{Role: principal.Role, Status: "enabled"}}}, Dependencies{Access: assignedBankAccess{bank: bank}, Catalog: catalog, Card: card}, nil)

	for _, tc := range []struct {
		name, path, body string
	}{
		{"product", "/v1/banks/" + bank + "/card-products", `{"product_code":"gold","name":"Gold","configuration":{}}`},
		{"client", "/v1/banks/" + bank + "/clients", `{"external_client_ref":"client"}`},
		{"account reference", "/v1/banks/" + bank + "/account-references", `{"client_id":"` + clientID + `","external_account_ref":"account"}`},
		{"issue card", "/v1/banks/" + bank + "/cards", `{"client_id":"` + clientID + `","account_reference_id":"` + accountID + `","product_id":"` + productID + `","reason":"issue"}`},
		{"replace card", "/v1/banks/" + bank + "/cards/" + cardID + `:replace`, `{"reason":"replace"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer token")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "bank-operator-"+tc.name)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusCreated {
				t.Fatalf("POST %s: got %d: %s", tc.path, w.Code, w.Body.String())
			}
		})
	}

	r := httptest.NewRequest(http.MethodPost, "/v1/banks/"+otherBank+"/clients", strings.NewReader(`{"external_client_ref":"other"}`))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "other-bank")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-bank create: got %d: %s", w.Code, w.Body.String())
	}
}

func TestBankOperatorCannotManageIssuerOnlyResources(t *testing.T) {
	const bank = "10000000-0000-4000-8000-000000000001"
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: "operator", EntityID: bank, UserSummary: auth.UserSummary{Role: "bank_operator", Status: "enabled"}}}, Dependencies{Access: assignedBankAccess{bank: bank}, Catalog: referenceHandlerService{}}, nil)
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"patch product", http.MethodPatch, "/v1/banks/" + bank + "/card-products/20000000-0000-4000-8000-000000000001", `{"name":"Changed"}`},
		{"create bank", http.MethodPost, "/v1/banks", `{"bank_reference":"other","name":"Other"}`},
		{"create user", http.MethodPost, "/v1/users", `{"username":"another","password":"ValidPassword!2026","role":"bank_operator","entity_id":"` + bank + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer token")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", "issuer-only-"+tc.name)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("%s %s: got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
			}
		})
	}
}
