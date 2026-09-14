package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

type UserRecord struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	EntityID     string
	Status       string
	AuthVersion  int64
}

func (u UserRecord) summary() UserSummary {
	return UserSummary{Username: u.Username, Status: u.Status, Role: u.Role}
}

type Service struct {
	store  Store
	signer *Signer
	now    func() time.Time
}

func NewService(store Store, signer *Signer) *Service {
	return &Service{store: store, signer: signer, now: time.Now}
}

func (s *Service) Login(ctx context.Context, username, password, requestID string) (TokenResponse, string, time.Time, error) {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	defer tx.Rollback(ctx)

	user, err := tx.UserByUsername(ctx, normalizeUsername(username), true)
	if err != nil || user.Status != "enabled" {
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "login", Outcome: "failed", RequestID: requestID, Details: map[string]string{"reason": "invalid_credentials"}}); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		return TokenResponse{}, "", time.Time{}, ErrInvalidCredentials
	}
	valid, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if !valid {
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "login", Outcome: "failed", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, RequestID: requestID, Details: map[string]string{"reason": "invalid_credentials"}}); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		return TokenResponse{}, "", time.Time{}, ErrInvalidCredentials
	}
	sessionID, err := newUUID()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	rawToken, err := newRefreshToken()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	expiresAt, err := tx.CreateSession(ctx, sessionID, user.ID, user.AuthVersion)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	refreshID, err := newUUID()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.CreateRefreshToken(ctx, refreshID, sessionID, tokenHash(rawToken), "", expiresAt); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.RecordAudit(ctx, AuditEvent{EventType: "login", Outcome: "succeeded", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, SessionID: sessionID, RequestID: requestID}); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	response, err := s.signer.Issue(user, sessionID)
	return response, rawToken, expiresAt, err
}

func (s *Service) Refresh(ctx context.Context, rawToken, requestID string) (TokenResponse, string, time.Time, error) {
	lookup, err := s.store.LookupRefresh(ctx, tokenHash(rawToken))
	if errors.Is(err, ErrNotFound) {
		if auditErr := s.recordUnknownRefresh(ctx, requestID); auditErr != nil {
			return TokenResponse{}, "", time.Time{}, auditErr
		}
		return TokenResponse{}, "", time.Time{}, ErrInvalidRefresh
	}
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	user, err := tx.UserByID(ctx, lookup.UserID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	session, err := tx.Session(ctx, lookup.SessionID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	token, err := tx.RefreshToken(ctx, lookup.TokenID, lookup.SessionID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	now := s.now().UTC()
	if token.Consumed || session.Revoked || user.Status != "enabled" || user.AuthVersion != session.AuthVersion || !now.Before(session.ExpiresAt) || !now.Before(token.ExpiresAt) {
		if token.Consumed && !session.Revoked {
			err = tx.RevokeSession(ctx, session.ID, now, "refresh_token_reuse")
			if err == nil {
				err = tx.RecordAudit(ctx, AuditEvent{EventType: "refresh_replay", Outcome: "rejected", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, SessionID: session.ID, RequestID: requestID})
			}
		} else {
			err = tx.RecordAudit(ctx, AuditEvent{EventType: "refresh", Outcome: "failed", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, SessionID: session.ID, RequestID: requestID, Details: map[string]string{"reason": "invalid_refresh_token"}})
		}
		if err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		return TokenResponse{}, "", time.Time{}, ErrInvalidRefresh
	}
	newToken, err := newRefreshToken()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.ConsumeRefreshToken(ctx, token.ID, now); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.TouchSession(ctx, session.ID, now); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	refreshID, err := newUUID()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.CreateRefreshToken(ctx, refreshID, session.ID, tokenHash(newToken), token.ID, session.ExpiresAt); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.RecordAudit(ctx, AuditEvent{EventType: "refresh", Outcome: "succeeded", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, SessionID: session.ID, RequestID: requestID}); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	response, err := s.signer.Issue(user, session.ID)
	return response, newToken, session.ExpiresAt, err
}

func (s *Service) Logout(ctx context.Context, rawToken, requestID string) error {
	lookup, err := s.store.LookupRefresh(ctx, tokenHash(rawToken))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := tx.UserByID(ctx, lookup.UserID, true)
	if err != nil {
		return err
	}
	session, err := tx.Session(ctx, lookup.SessionID, true)
	if err != nil {
		return err
	}
	if _, err := tx.RefreshToken(ctx, lookup.TokenID, lookup.SessionID, true); err != nil {
		return err
	}
	if !session.Revoked {
		if err := tx.RevokeSession(ctx, session.ID, s.now().UTC(), "logout"); err != nil {
			return err
		}
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "logout", Outcome: "succeeded", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, SessionID: session.ID, RequestID: requestID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) ChangePassword(ctx context.Context, claims Claims, currentPassword, newPassword, requestID string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := tx.UserByID(ctx, claims.UserID, true)
	if errors.Is(err, ErrNotFound) {
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "password_change", Outcome: "rejected", RequestID: requestID, Details: map[string]string{"reason": "invalid_current_password"}}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrInvalidPassword
	}
	if err != nil {
		return err
	}
	if user.Status != "enabled" {
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "password_change", Outcome: "rejected", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, RequestID: requestID, Details: map[string]string{"reason": "invalid_current_password"}}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrInvalidPassword
	}
	valid, err := VerifyPassword(user.PasswordHash, currentPassword)
	if err != nil {
		return err
	}
	if !valid {
		if err := tx.RecordAudit(ctx, AuditEvent{EventType: "password_change", Outcome: "failed", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, RequestID: requestID, Details: map[string]string{"reason": "invalid_current_password"}}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrInvalidPassword
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	if err := tx.UpdatePassword(ctx, user.ID, hash); err != nil {
		return err
	}
	if err := tx.RecordAudit(ctx, AuditEvent{EventType: "password_change", Outcome: "succeeded", ActorID: user.ID, SubjectID: user.ID, EntityID: user.EntityID, RequestID: requestID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ValidateAccess(token string) (Claims, error) { return s.signer.Validate(token) }

func (s *Service) recordUnknownRefresh(ctx context.Context, requestID string) error {
	tx, err := s.store.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tx.RecordAudit(ctx, AuditEvent{EventType: "refresh", Outcome: "failed", RequestID: requestID, Details: map[string]string{"reason": "invalid_refresh_token"}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func newRefreshToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func tokenHash(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }
