package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"card-issuer-api/internal/auth"
)

type requestIDContextKey struct{}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validUUID(requestID) {
			requestID = randomUUID()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID)))
	})
}

func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDContextKey{}).(string)
	return value
}

// RequestID returns the trusted request identifier installed by the API middleware.
func RequestID(r *http.Request) string { return requestID(r) }

func writeProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "urn:card-issuer-api:error:" + code, Title: http.StatusText(status), Status: status, Code: code, Detail: detail, RequestID: requestID(r)})
}

// WriteProblem is available to business handlers so every API failure has one shape.
func WriteProblem(w http.ResponseWriter, r *http.Request, status int, code, detail string) {
	writeProblem(w, r, status, code, detail)
}

func apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, r, http.StatusNotFound, "not_found", "The requested API endpoint does not exist.")
}

type authenticationHandler struct{ service auth.API }
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h authenticationHandler) login(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var input loginRequest
	if !decodeRequest(w, r, &input) || input.Username == "" || input.Password == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "username and password are required.")
		return
	}
	response, refresh, expiresAt, err := h.service.Login(r.Context(), input.Username, input.Password, requestID(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeProblem(w, r, http.StatusUnauthorized, "invalid_credentials", "The supplied credentials are invalid.")
		return
	}
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
		return
	}
	setRefreshCookie(w, refresh, expiresAt)
	writeJSON(w, http.StatusOK, response)
}

func (h authenticationHandler) refresh(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	cookie, err := r.Cookie(auth.RefreshCookieName)
	if err != nil || cookie.Value == "" {
		clearRefreshCookie(w)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_refresh_token", "The refresh token is invalid.")
		return
	}
	response, refresh, expiresAt, err := h.service.Refresh(r.Context(), cookie.Value, requestID(r))
	if errors.Is(err, auth.ErrInvalidRefresh) {
		clearRefreshCookie(w)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_refresh_token", "The refresh token is invalid.")
		return
	}
	if err != nil {
		clearRefreshCookie(w)
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
		return
	}
	setRefreshCookie(w, refresh, expiresAt)
	writeJSON(w, http.StatusOK, response)
}

func (h authenticationHandler) logout(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if cookie, err := r.Cookie(auth.RefreshCookieName); err == nil && cookie.Value != "" {
		if err := h.service.Logout(r.Context(), cookie.Value, requestID(r)); err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
			return
		}
	}
	clearRefreshCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h authenticationHandler) me(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, claims.UserSummary)
}

func (h authenticationHandler) password(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	var input passwordRequest
	if !decodeRequest(w, r, &input) || input.CurrentPassword == "" || input.NewPassword == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "current_password and new_password are required.")
		return
	}
	err := h.service.ChangePassword(r.Context(), claims, input.CurrentPassword, input.NewPassword, requestID(r))
	switch {
	case errors.Is(err, auth.ErrPasswordPolicy):
		writeProblem(w, r, http.StatusBadRequest, "invalid_password", "new_password must meet the password policy.")
	case errors.Is(err, auth.ErrInvalidPassword):
		writeProblem(w, r, http.StatusUnauthorized, "invalid_credentials", "The supplied credentials are invalid.")
	case err != nil:
		writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	default:
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (h authenticationHandler) authenticate(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	value := r.Header.Get("Authorization")
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	claims, err := h.service.ValidateAccess(parts[1])
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	return claims, true
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeProblem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "The request method is not allowed for this endpoint.")
	return false
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return errors.Is(decoder.Decode(&struct{}{}), io.EOF)
}

func setRefreshCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{Name: auth.RefreshCookieName, Value: token, Path: "/", Expires: expiresAt.UTC(), MaxAge: int(time.Until(expiresAt).Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: auth.RefreshCookieName, Value: "", Path: "/", Expires: time.Unix(1, 0).UTC(), MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func validUUID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func randomUUID() string {
	bytes := make([]byte, 16)
	_, _ = rand.Read(bytes)
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	hexValue := hex.EncodeToString(bytes)
	return hexValue[0:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:]
}
