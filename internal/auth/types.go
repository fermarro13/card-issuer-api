package auth

import (
	"context"
	"errors"
	"time"
)

const RefreshCookieName = "__Host-card-issuer-refresh"

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidRefresh     = errors.New("invalid refresh token")
	ErrInvalidAccess      = errors.New("invalid access token")
	ErrInvalidPassword    = errors.New("invalid current password")
	ErrPasswordPolicy     = errors.New("password does not meet policy")
	ErrNotFound           = errors.New("auth record not found")
)

type UserSummary struct {
	Username string `json:"username"`
	Status   string `json:"status"`
	Role     string `json:"role"`
}

type Claims struct {
	UserSummary
	UserID    string
	SessionID string
	EntityID  string
}

// Principal is the application-facing identity of an authenticated caller.
// It deliberately excludes JWT and session details, which belong to the
// authentication transport rather than business workflows.
type Principal struct {
	UserID   string
	Username string
	Role     string
	EntityID string
}

// Principal returns the identity fields that application services need after
// the access token has been verified.
func (c Claims) Principal() Principal {
	return Principal{
		UserID:   c.UserID,
		Username: c.Username,
		Role:     c.Role,
		EntityID: c.EntityID,
	}
}

type TokenResponse struct {
	AccessToken string      `json:"access_token"`
	TokenType   string      `json:"token_type"`
	ExpiresAt   string      `json:"expires_at"`
	User        UserSummary `json:"user"`
}

type API interface {
	Login(context.Context, string, string, string) (TokenResponse, string, time.Time, error)
	Refresh(context.Context, string, string) (TokenResponse, string, time.Time, error)
	Logout(context.Context, string, string) error
	ChangePassword(context.Context, Claims, string, string, string) error
	ValidateAccess(string) (Claims, error)
}
