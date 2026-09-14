package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	pool   *pgxpool.Pool
	signer *Signer
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, signer *Signer) *Service {
	return &Service{pool: pool, signer: signer, now: time.Now}
}

func (s *Service) Login(ctx context.Context, username, password, requestID string) (TokenResponse, string, time.Time, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	user, err := loadUserByUsername(ctx, tx, normalizeUsername(username), true)
	if err != nil || user.Status != "enabled" {
		if err := insertAudit(ctx, tx, "login", "failed", "", "", "", "", requestID, map[string]string{"reason": "invalid_credentials"}); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TokenResponse{}, "", time.Time{}, err
		}
		return TokenResponse{}, "", time.Time{}, ErrInvalidCredentials
	}
	valid, verifyErr := VerifyPassword(user.PasswordHash, password)
	if verifyErr != nil {
		return TokenResponse{}, "", time.Time{}, verifyErr
	}
	if !valid {
		if err := insertAudit(ctx, tx, "login", "failed", user.ID, user.ID, user.EntityID, "", requestID, map[string]string{"reason": "invalid_credentials"}); err != nil {
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
	var expiresAt time.Time
	if err := tx.QueryRow(ctx, "INSERT INTO control.auth_sessions(id,user_id,auth_version) VALUES ($1,$2,$3) RETURNING expires_at", sessionID, user.ID, user.AuthVersion).Scan(&expiresAt); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	refreshID, err := newUUID()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO control.refresh_tokens(id,session_id,token_hash,expires_at) VALUES ($1,$2,$3,$4)", refreshID, sessionID, tokenHash(rawToken), expiresAt); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := insertAudit(ctx, tx, "login", "succeeded", user.ID, user.ID, user.EntityID, sessionID, requestID, nil); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	response, err := s.signer.Issue(user, sessionID)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	return response, rawToken, expiresAt, nil
}

func (s *Service) Refresh(ctx context.Context, rawToken, requestID string) (TokenResponse, string, time.Time, error) {
	tokenID, sessionID, userID, err := s.lookupRefresh(ctx, rawToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if auditErr := s.recordUnknownRefresh(ctx, requestID); auditErr != nil {
				return TokenResponse{}, "", time.Time{}, auditErr
			}
			return TokenResponse{}, "", time.Time{}, ErrInvalidRefresh
		}
		return TokenResponse{}, "", time.Time{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	defer tx.Rollback(ctx)
	user, err := loadUserByID(ctx, tx, userID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	session, err := loadSession(ctx, tx, sessionID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	token, err := loadRefreshToken(ctx, tx, tokenID, sessionID, true)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	now := s.now().UTC()
	if token.Consumed || session.Revoked || user.Status != "enabled" || user.AuthVersion != session.AuthVersion || !now.Before(session.ExpiresAt) || !now.Before(token.ExpiresAt) {
		if token.Consumed && !session.Revoked {
			if _, err := tx.Exec(ctx, "UPDATE control.auth_sessions SET revoked_at=$2,revocation_reason='refresh_token_reuse' WHERE id=$1 AND revoked_at IS NULL", session.ID, now); err != nil {
				return TokenResponse{}, "", time.Time{}, err
			}
			if err := insertAudit(ctx, tx, "refresh_replay", "rejected", user.ID, user.ID, user.EntityID, session.ID, requestID, nil); err != nil {
				return TokenResponse{}, "", time.Time{}, err
			}
		} else if err := insertAudit(ctx, tx, "refresh", "failed", user.ID, user.ID, user.EntityID, session.ID, requestID, map[string]string{"reason": "invalid_refresh_token"}); err != nil {
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
	if _, err := tx.Exec(ctx, "UPDATE control.refresh_tokens SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL", token.ID, now); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, "UPDATE control.auth_sessions SET last_refreshed_at=$2 WHERE id=$1", session.ID, now); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	refreshID, err := newUUID()
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO control.refresh_tokens(id,session_id,token_hash,parent_token_id,expires_at) VALUES ($1,$2,$3,$4,$5)", refreshID, session.ID, tokenHash(newToken), token.ID, session.ExpiresAt); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := insertAudit(ctx, tx, "refresh", "succeeded", user.ID, user.ID, user.EntityID, session.ID, requestID, nil); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	response, err := s.signer.Issue(user, session.ID)
	if err != nil {
		return TokenResponse{}, "", time.Time{}, err
	}
	return response, newToken, session.ExpiresAt, nil
}

func (s *Service) Logout(ctx context.Context, rawToken, requestID string) error {
	tokenID, sessionID, userID, err := s.lookupRefresh(ctx, rawToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := loadUserByID(ctx, tx, userID, true)
	if err != nil {
		return err
	}
	session, err := loadSession(ctx, tx, sessionID, true)
	if err != nil {
		return err
	}
	if _, err := loadRefreshToken(ctx, tx, tokenID, sessionID, true); err != nil {
		return err
	}
	if !session.Revoked {
		if _, err := tx.Exec(ctx, "UPDATE control.auth_sessions SET revoked_at=$2,revocation_reason='logout' WHERE id=$1 AND revoked_at IS NULL", session.ID, s.now().UTC()); err != nil {
			return err
		}
		if err := insertAudit(ctx, tx, "logout", "succeeded", user.ID, user.ID, user.EntityID, session.ID, requestID, nil); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) ChangePassword(ctx context.Context, claims Claims, currentPassword, newPassword, requestID string) error {
	if err := ValidateNewPassword(newPassword); err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	user, err := loadUserByID(ctx, tx, claims.UserID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := insertAudit(ctx, tx, "password_change", "rejected", "", "", "", "", requestID, map[string]string{"reason": "invalid_current_password"}); err != nil {
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
		if err := insertAudit(ctx, tx, "password_change", "rejected", user.ID, user.ID, user.EntityID, "", requestID, map[string]string{"reason": "invalid_current_password"}); err != nil {
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
		if err := insertAudit(ctx, tx, "password_change", "failed", user.ID, user.ID, user.EntityID, "", requestID, map[string]string{"reason": "invalid_current_password"}); err != nil {
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
	if _, err := tx.Exec(ctx, "UPDATE control.users SET password_hash=$2,updated_by=$1 WHERE id=$1", user.ID, hash); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, "password_change", "succeeded", user.ID, user.ID, user.EntityID, "", requestID, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) ValidateAccess(token string) (Claims, error) { return s.signer.Validate(token) }

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func newRefreshToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func tokenHash(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }

func (s *Service) lookupRefresh(ctx context.Context, rawToken string) (string, string, string, error) {
	var tokenID, sessionID, userID string
	err := s.pool.QueryRow(ctx, "SELECT t.id::text,t.session_id::text,s.user_id::text FROM control.refresh_tokens t JOIN control.auth_sessions s ON s.id=t.session_id WHERE t.token_hash=$1", tokenHash(rawToken)).Scan(&tokenID, &sessionID, &userID)
	return tokenID, sessionID, userID, err
}

func (s *Service) recordUnknownRefresh(ctx context.Context, requestID string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := insertAudit(ctx, tx, "refresh", "failed", "", "", "", "", requestID, map[string]string{"reason": "invalid_refresh_token"}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func loadUserByUsername(ctx context.Context, tx pgx.Tx, username string, lock bool) (UserRecord, error) {
	query := "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE normalized_username=$1"
	if lock {
		query += " FOR UPDATE"
	}
	return scanUser(tx.QueryRow(ctx, query, username))
}

func loadUserByID(ctx context.Context, tx pgx.Tx, id string, lock bool) (UserRecord, error) {
	query := "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	return scanUser(tx.QueryRow(ctx, query, id))
}

func scanUser(row pgx.Row) (UserRecord, error) {
	var user UserRecord
	err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.EntityID, &user.Status, &user.AuthVersion)
	return user, err
}

type sessionRecord struct {
	ID          string
	AuthVersion int64
	ExpiresAt   time.Time
	Revoked     bool
}

func loadSession(ctx context.Context, tx pgx.Tx, id string, lock bool) (sessionRecord, error) {
	query := "SELECT id::text,auth_version,expires_at,revoked_at IS NOT NULL FROM control.auth_sessions WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	var session sessionRecord
	err := tx.QueryRow(ctx, query, id).Scan(&session.ID, &session.AuthVersion, &session.ExpiresAt, &session.Revoked)
	return session, err
}

type refreshTokenRecord struct {
	ID        string
	ExpiresAt time.Time
	Consumed  bool
}

func loadRefreshToken(ctx context.Context, tx pgx.Tx, id, sessionID string, lock bool) (refreshTokenRecord, error) {
	query := "SELECT id::text,expires_at,consumed_at IS NOT NULL FROM control.refresh_tokens WHERE id=$1 AND session_id=$2"
	if lock {
		query += " FOR UPDATE"
	}
	var token refreshTokenRecord
	err := tx.QueryRow(ctx, query, id, sessionID).Scan(&token.ID, &token.ExpiresAt, &token.Consumed)
	return token, err
}

func insertAudit(ctx context.Context, tx pgx.Tx, eventType, outcome, actorID, subjectID, entityID, sessionID, requestID string, details map[string]string) error {
	payload := []byte("{}")
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		payload = encoded
	}
	var actor any
	if actorID != "" {
		actor = actorID
	}
	var subject any
	if subjectID != "" {
		subject = subjectID
	}
	var entity any
	if entityID != "" {
		entity = entityID
	}
	var session any
	if sessionID != "" {
		session = sessionID
	}
	_, err := tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,session_id,request_id,details) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)", eventType, outcome, actor, subject, entity, session, requestID, payload)
	return err
}
