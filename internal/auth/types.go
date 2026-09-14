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
