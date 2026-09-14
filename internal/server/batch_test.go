package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"card-issuer-api/internal/auth"
)

type batchHandlerService struct {
	create  func(context.Context, auth.Principal, string, string, []byte, string, string, []string, string) (json.RawMessage, error)
	execute func(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
	cancel  func(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
	retry   func(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
}

func (batchHandlerService) ListCardStatusBatches(context.Context, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (batchHandlerService) GetCardStatusBatch(context.Context, string, string) (json.RawMessage, error) {
	return nil, nil
}
func (batchHandlerService) ListCardStatusBatchItems(context.Context, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (s batchHandlerService) CreateCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, targetStatus, reason string, cardIDs []string, requestID string) (json.RawMessage, error) {
	return s.create(ctx, principal, bank, key, body, targetStatus, reason, cardIDs, requestID)
}
func (s batchHandlerService) ExecuteCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	return s.execute(ctx, principal, bank, id, key, body, requestID)
}
func (s batchHandlerService) CancelCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	return s.cancel(ctx, principal, bank, id, key, body, requestID)
}
func (s batchHandlerService) RetryCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	return s.retry(ctx, principal, bank, id, key, body, requestID)
}

func TestCardStatusBatchHandlersUseTypedWorkflows(t *testing.T) {
	bank := "10000000-0000-4000-8000-000000000001"
	batch := "20000000-0000-4000-8000-000000000001"
	first := "30000000-0000-4000-8000-000000000001"
	second := "40000000-0000-4000-8000-000000000001"
	service := batchHandlerService{
		create: func(_ context.Context, _ auth.Principal, gotBank, key string, _ []byte, targetStatus, reason string, cardIDs []string, _ string) (json.RawMessage, error) {
			if gotBank != bank || key != "create-key" || targetStatus != "suspended" || reason != "fraud review" || len(cardIDs) != 2 || cardIDs[0] != second || cardIDs[1] != first {
				t.Fatal("unexpected batch-create workflow inputs")
			}
			return json.RawMessage(`{"id":"` + batch + `","status":"draft"}`), nil
		},
		execute: func(_ context.Context, _ auth.Principal, gotBank, id, key string, _ []byte, _ string) (json.RawMessage, error) {
			if gotBank != bank || id != batch || key != "execute-key" {
				t.Fatal("unexpected batch-execute workflow inputs")
			}
			return json.RawMessage(`{"id":"` + batch + `","status":"queued"}`), nil
		},
		cancel: func(_ context.Context, _ auth.Principal, gotBank, id, key string, _ []byte, _ string) (json.RawMessage, error) {
			if gotBank != bank || id != batch || key != "cancel-key" {
				t.Fatal("unexpected batch-cancel workflow inputs")
			}
			return json.RawMessage(`{"id":"` + batch + `","status":"cancelled"}`), nil
		},
		retry: func(_ context.Context, _ auth.Principal, gotBank, id, key string, _ []byte, _ string) (json.RawMessage, error) {
			if gotBank != bank || id != batch || key != "retry-key" {
				t.Fatal("unexpected batch-retry workflow inputs")
			}
			return json.RawMessage(`{"id":"` + batch + `","status":"draft"}`), nil
		},
	}
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: "operator", UserSummary: auth.UserSummary{Role: "issuer_operator", Status: "enabled"}}}, Dependencies{Access: allowBankAccess{}, Batch: service}, []byte("01234567890123456789012345678901"))
	for _, test := range []struct {
		name, path, key, body string
		want                  int
	}{
		{"create", "/v1/banks/" + bank + "/card-status-batches", "create-key", `{"target_status":"suspended","reason":"fraud review","card_ids":["` + second + `","` + first + `"]}`, http.StatusCreated},
		{"execute", "/v1/banks/" + bank + "/card-status-batches/" + batch + ":execute", "execute-key", `{}`, http.StatusAccepted},
		{"cancel", "/v1/banks/" + bank + "/card-status-batches/" + batch + ":cancel", "cancel-key", `{}`, http.StatusOK},
		{"retry", "/v1/banks/" + bank + "/card-status-batches/" + batch + ":retry", "retry-key", `{}`, http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer token")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Idempotency-Key", test.key)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCardStatusBatchMutationRejectsReadOnlyRole(t *testing.T) {
	bank := "10000000-0000-4000-8000-000000000001"
	handler := NewResourceHandler(handlerAuth{claims: auth.Claims{UserID: "reader", EntityID: bank, UserSummary: auth.UserSummary{Role: "bank_readonly", Status: "enabled"}}}, Dependencies{Access: allowBankAccess{}, Batch: batchHandlerService{}}, nil)
	r := httptest.NewRequest(http.MethodPost, "/v1/banks/"+bank+"/card-status-batches", strings.NewReader(`{"target_status":"suspended","reason":"review","card_ids":["30000000-0000-4000-8000-000000000001"]}`))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "key")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
}
