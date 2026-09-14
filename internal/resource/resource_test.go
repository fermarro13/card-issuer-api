package resource

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

type testAuth struct {
	claims auth.Claims
	err    error
}

func (t testAuth) Login(context.Context, string, string, string) (auth.TokenResponse, string, time.Time, error) {
	return auth.TokenResponse{}, "", time.Time{}, errors.New("unused")
}
func (t testAuth) Refresh(context.Context, string, string) (auth.TokenResponse, string, time.Time, error) {
	return auth.TokenResponse{}, "", time.Time{}, errors.New("unused")
}
func (t testAuth) Logout(context.Context, string, string) error { return errors.New("unused") }
func (t testAuth) ChangePassword(context.Context, auth.Claims, string, string, string) error {
	return errors.New("unused")
}
func (t testAuth) ValidateAccess(string) (auth.Claims, error) { return t.claims, t.err }

func TestDirectoryAndStaffRejectNonIssuerOperator(t *testing.T) {
	a := New(testAuth{claims: auth.Claims{UserSummary: auth.UserSummary{Role: "issuer_readonly", Status: "enabled"}}}, nil, nil, nil, nil, nil, "shard_01", make([]byte, 32))
	for _, path := range []string{"/v1/banks", "/v1/users"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer token")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get("Content-Type") != "application/problem+json" {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestMissingIdempotencyKeyIsRejectedBeforeMutation(t *testing.T) {
	a := New(testAuth{claims: auth.Claims{UserSummary: auth.UserSummary{Role: "issuer_operator", Status: "enabled"}}}, nil, nil, nil, nil, nil, "shard_01", make([]byte, 32))
	r := httptest.NewRequest(http.MethodPost, "/v1/users", strings.NewReader(`{"username":"new.user","password":"ValidPassword!2026","role":"issuer_readonly","entity_id":""}`))
	r.Header.Set("Authorization", "Bearer token")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestCursorIsTamperAndScopeBound(t *testing.T) {
	a := New(nil, nil, nil, nil, nil, nil, "shard_01", []byte("01234567890123456789012345678901"))
	token := a.cursorEncode(cursor{Kind: "cards", Bank: "10000000-0000-4000-8000-000000000001", Filter: "status=active", Created: "2026-09-13T00:00:00Z", ID: "20000000-0000-4000-8000-000000000001"})
	if _, err := a.cursorDecode(token, "cards", "10000000-0000-4000-8000-000000000001", "status=active"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cursorDecode(token, "cards", "10000000-0000-4000-8000-000000000001", "status=suspended"); err == nil {
		t.Fatal("cursor was not filter-bound")
	}
	if _, err := a.cursorDecode(token+"x", "cards", "10000000-0000-4000-8000-000000000001", "status=active"); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestCollectionCursorUsesTimestampOrderIncludingFractions(t *testing.T) {
	a := New(nil, nil, nil, nil, nil, nil, "shard_01", []byte("01234567890123456789012345678901"))
	data := []json.RawMessage{
		json.RawMessage(`{"id":"20000000-0000-4000-8000-000000000001","created_at":"2026-09-13T00:00:00.1Z"}`),
		json.RawMessage(`{"id":"20000000-0000-4000-8000-000000000002","created_at":"2026-09-13T00:00:00Z"}`),
	}
	token := a.cursorEncode(cursor{Kind: "cards", Bank: "bank", Filter: "", Created: "2026-09-13T00:00:00.1Z", ID: "20000000-0000-4000-8000-000000000001"})
	r := httptest.NewRequest(http.MethodGet, "/v1/banks/bank/cards?cursor="+token, nil)
	w := httptest.NewRecorder()
	a.collection(w, r, "cards", "bank", data)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "000000000002") {
		t.Fatalf("fractional-time page was skipped: %d %s", w.Code, w.Body.String())
	}
}

func TestProductConfigurationMustBeJSONObject(t *testing.T) {
	if object(nil) || object(json.RawMessage(`null`)) || !object(json.RawMessage(`{"limit":1}`)) {
		t.Fatal("product configuration object validation is incorrect")
	}
}

func TestTransitionTable(t *testing.T) {
	for _, tc := range []struct {
		from, action   string
		valid, ignored bool
	}{{"issued", "activate", true, false}, {"active", "suspend", true, false}, {"active", "resume", true, true}, {"active", "close", true, false}, {"closed", "close", true, true}, {"expired", "activate", false, false}} {
		_, valid, ignored := transition(tc.from, tc.action)
		if valid != tc.valid || ignored != tc.ignored {
			t.Fatalf("%s/%s: valid=%v ignored=%v", tc.from, tc.action, valid, ignored)
		}
	}
}
