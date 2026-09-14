package server

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"card-issuer-api/internal/auth"
)

func authTestHandler(t *testing.T) (http.Handler, string) {
	t.Helper()
	signer, err := auth.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "issuer", "audience")
	if err != nil {
		t.Fatal(err)
	}
	response, err := signer.Issue(auth.UserRecord{ID: "20000000-0000-4000-8000-000000000001", Username: "operator", Role: "issuer_operator", Status: "enabled"}, "30000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	return Handler(func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil }, auth.NewService(nil, signer)), response.AccessToken
}

func TestMeIsStatelessAndUsesRequestIDs(t *testing.T) {
	handler, token := authTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", "40000000-0000-4000-8000-000000000001")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") != request.Header.Get("X-Request-ID") || strings.Contains(response.Body.String(), "access_token") {
		t.Fatalf("unexpected me response: %d %s", response.Code, response.Body.String())
	}
	var summary auth.UserSummary
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil || summary.Username != "operator" || summary.Status != "enabled" || summary.Role != "issuer_operator" {
		t.Fatalf("unexpected user summary: %#v %v", summary, err)
	}
}

func TestAuthenticationErrorsUseProblemsAndBearerChallenge(t *testing.T) {
	handler, _ := authTestHandler(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/me", nil))
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Header().Get("WWW-Authenticate"), "Bearer") || response.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unexpected bearer failure: %d %#v", response.Code, response.Header())
	}
	var body problem
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.Code != "invalid_access_token" || !validUUID(body.RequestID) {
		t.Fatalf("invalid problem: %#v %v", body, err)
	}
}

func TestStrictJSONAndRefreshCookieContract(t *testing.T) {
	handler, token := authTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/me/password", strings.NewReader(`{"current_password":"x","new_password":"Abcdefghijk1!","extra":true}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected strict JSON response: %d", response.Code)
	}
	cookieResponse := httptest.NewRecorder()
	setRefreshCookie(cookieResponse, "opaque", time.Now().Add(time.Hour))
	cookies := cookieResponse.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != auth.RefreshCookieName || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/" || cookies[0].Domain != "" {
		t.Fatalf("unexpected refresh cookie: %#v", cookies)
	}
}

type fakeAuth struct {
	claims       auth.Claims
	response     auth.TokenResponse
	refreshToken string
	expiresAt    time.Time
	loginErr     error
	refreshErr   error
	logoutToken  string
	logoutErr    error
	changed      bool
	passwordErr  error
}

func (f *fakeAuth) Login(context.Context, string, string, string) (auth.TokenResponse, string, time.Time, error) {
	return f.response, f.refreshToken, f.expiresAt, f.loginErr
}
func (f *fakeAuth) Refresh(context.Context, string, string) (auth.TokenResponse, string, time.Time, error) {
	return f.response, f.refreshToken, f.expiresAt, f.refreshErr
}
func (f *fakeAuth) Logout(_ context.Context, token, _ string) error {
	f.logoutToken = token
	return f.logoutErr
}
func (f *fakeAuth) ChangePassword(context.Context, auth.Claims, string, string, string) error {
	f.changed = true
	return f.passwordErr
}
func (f *fakeAuth) ValidateAccess(string) (auth.Claims, error) { return f.claims, nil }

func TestAuthenticationEndpointContracts(t *testing.T) {
	fake := &fakeAuth{claims: auth.Claims{UserSummary: auth.UserSummary{Username: "operator", Status: "enabled", Role: "issuer_operator"}}, response: auth.TokenResponse{AccessToken: "access", TokenType: "Bearer", ExpiresAt: "2026-09-13T00:15:00Z", User: auth.UserSummary{Username: "operator", Status: "enabled", Role: "issuer_operator"}}, refreshToken: "refresh", expiresAt: time.Now().Add(time.Hour)}
	handler := Handler(func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil }, fake)
	login := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"operator","password":"password"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || strings.Contains(loginResponse.Body.String(), "refresh") {
		t.Fatalf("unexpected login response: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	if cookies := loginResponse.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != auth.RefreshCookieName || cookies[0].Value != "refresh" {
		t.Fatalf("login did not set refresh cookie: %#v", cookies)
	}

	refresh := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	refresh.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: "old"})
	refreshResponse := httptest.NewRecorder()
	handler.ServeHTTP(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusOK || strings.Contains(refreshResponse.Body.String(), "refresh") {
		t.Fatalf("unexpected refresh response: %d %s", refreshResponse.Code, refreshResponse.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	logout.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: "old"})
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusNoContent || fake.logoutToken != "old" {
		t.Fatalf("unexpected logout response: %d", logoutResponse.Code)
	}
	if cookies := logoutResponse.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear refresh cookie: %#v", cookies)
	}

	password := httptest.NewRequest(http.MethodPost, "/v1/me/password", strings.NewReader(`{"current_password":"old","new_password":"ValidPassword!2026"}`))
	password.Header.Set("Authorization", "Bearer access")
	password.Header.Set("Content-Type", "application/json")
	passwordResponse := httptest.NewRecorder()
	handler.ServeHTTP(passwordResponse, password)
	if passwordResponse.Code != http.StatusNoContent || !fake.changed {
		t.Fatalf("unexpected password response: %d", passwordResponse.Code)
	}

	fake.refreshErr = errors.New("database unavailable")
	failure := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	failure.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: "old"})
	failureResponse := httptest.NewRecorder()
	handler.ServeHTTP(failureResponse, failure)
	if failureResponse.Code != http.StatusInternalServerError || len(failureResponse.Result().Cookies()) != 1 || failureResponse.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("refresh failure did not clear cookie: %d %#v", failureResponse.Code, failureResponse.Result().Cookies())
	}
}

func TestAuthenticationEndpointErrorContracts(t *testing.T) {
	fake := &fakeAuth{claims: auth.Claims{UserSummary: auth.UserSummary{Username: "operator", Status: "enabled", Role: "issuer_operator"}}, refreshErr: auth.ErrInvalidRefresh, loginErr: auth.ErrInvalidCredentials, passwordErr: auth.ErrPasswordPolicy}
	handler := Handler(func(context.Context) error { return nil }, func(context.Context) error { return nil }, func(context.Context) error { return nil }, fake)
	login := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"operator","password":"wrong"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusUnauthorized || !strings.Contains(loginResponse.Body.String(), "invalid_credentials") {
		t.Fatalf("unexpected login error: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	refresh := httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil)
	refresh.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: "old"})
	refreshResponse := httptest.NewRecorder()
	handler.ServeHTTP(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusUnauthorized || len(refreshResponse.Result().Cookies()) != 1 || refreshResponse.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("unexpected refresh error: %d %#v", refreshResponse.Code, refreshResponse.Result().Cookies())
	}
	fake.logoutErr = errors.New("database unavailable")
	logout := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	logout.AddCookie(&http.Cookie{Name: auth.RefreshCookieName, Value: "old"})
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected logout error: %d", logoutResponse.Code)
	}
	password := httptest.NewRequest(http.MethodPost, "/v1/me/password", strings.NewReader(`{"current_password":"old","new_password":"short"}`))
	password.Header.Set("Authorization", "Bearer access")
	password.Header.Set("Content-Type", "application/json")
	passwordResponse := httptest.NewRecorder()
	handler.ServeHTTP(passwordResponse, password)
	if passwordResponse.Code != http.StatusBadRequest || !strings.Contains(passwordResponse.Body.String(), "invalid_password") {
		t.Fatalf("unexpected password error: %d %s", passwordResponse.Code, passwordResponse.Body.String())
	}
}
