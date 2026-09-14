package authrepository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"card-issuer-api/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Begin(ctx context.Context) (auth.Transaction, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin auth transaction: %w", err)
	}
	return transaction{tx: tx}, nil
}

func (s *Store) LookupRefresh(ctx context.Context, hash []byte) (auth.RefreshLookup, error) {
	var lookup auth.RefreshLookup
	err := s.pool.QueryRow(ctx, "SELECT t.id::text,t.session_id::text,s.user_id::text FROM control.refresh_tokens t JOIN control.auth_sessions s ON s.id=t.session_id WHERE t.token_hash=$1", hash).Scan(&lookup.TokenID, &lookup.SessionID, &lookup.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.RefreshLookup{}, auth.ErrNotFound
	}
	if err != nil {
		return auth.RefreshLookup{}, fmt.Errorf("lookup refresh token: %w", err)
	}
	return lookup, nil
}

type transaction struct{ tx pgx.Tx }

func (t transaction) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t transaction) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t transaction) UserByUsername(ctx context.Context, username string, lock bool) (auth.UserRecord, error) {
	query := "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE normalized_username=$1"
	if lock {
		query = "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE normalized_username=$1 FOR UPDATE"
	}
	return scanUser(t.tx.QueryRow(ctx, query, username))
}

func (t transaction) UserByID(ctx context.Context, id string, lock bool) (auth.UserRecord, error) {
	query := "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE id=$1"
	if lock {
		query = "SELECT id::text,normalized_username,password_hash,role,COALESCE(entity_id::text,''),status,auth_version FROM control.users WHERE id=$1 FOR UPDATE"
	}
	return scanUser(t.tx.QueryRow(ctx, query, id))
}

func (t transaction) Session(ctx context.Context, id string, lock bool) (auth.SessionRecord, error) {
	query := "SELECT id::text,auth_version,expires_at,revoked_at IS NOT NULL FROM control.auth_sessions WHERE id=$1"
	if lock {
		query = "SELECT id::text,auth_version,expires_at,revoked_at IS NOT NULL FROM control.auth_sessions WHERE id=$1 FOR UPDATE"
	}
	var session auth.SessionRecord
	err := t.tx.QueryRow(ctx, query, id).Scan(&session.ID, &session.AuthVersion, &session.ExpiresAt, &session.Revoked)
	return session, mapNotFound(err)
}

func (t transaction) RefreshToken(ctx context.Context, id, sessionID string, lock bool) (auth.RefreshTokenRecord, error) {
	query := "SELECT id::text,expires_at,consumed_at IS NOT NULL FROM control.refresh_tokens WHERE id=$1 AND session_id=$2"
	if lock {
		query = "SELECT id::text,expires_at,consumed_at IS NOT NULL FROM control.refresh_tokens WHERE id=$1 AND session_id=$2 FOR UPDATE"
	}
	var token auth.RefreshTokenRecord
	err := t.tx.QueryRow(ctx, query, id, sessionID).Scan(&token.ID, &token.ExpiresAt, &token.Consumed)
	return token, mapNotFound(err)
}

func (t transaction) CreateSession(ctx context.Context, id, userID string, version int64) (time.Time, error) {
	var expiresAt time.Time
	err := t.tx.QueryRow(ctx, "INSERT INTO control.auth_sessions(id,user_id,auth_version) VALUES ($1,$2,$3) RETURNING expires_at", id, userID, version).Scan(&expiresAt)
	return expiresAt, err
}

func (t transaction) CreateRefreshToken(ctx context.Context, id, sessionID string, hash []byte, parentID string, expiresAt time.Time) error {
	if parentID == "" {
		_, err := t.tx.Exec(ctx, "INSERT INTO control.refresh_tokens(id,session_id,token_hash,expires_at) VALUES ($1,$2,$3,$4)", id, sessionID, hash, expiresAt)
		return err
	}
	_, err := t.tx.Exec(ctx, "INSERT INTO control.refresh_tokens(id,session_id,token_hash,parent_token_id,expires_at) VALUES ($1,$2,$3,$4,$5)", id, sessionID, hash, parentID, expiresAt)
	return err
}

func (t transaction) RevokeSession(ctx context.Context, id string, at time.Time, reason string) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.auth_sessions SET revoked_at=$2,revocation_reason=$3 WHERE id=$1 AND revoked_at IS NULL", id, at, reason)
	return err
}

func (t transaction) ConsumeRefreshToken(ctx context.Context, id string, at time.Time) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.refresh_tokens SET consumed_at=$2 WHERE id=$1 AND consumed_at IS NULL", id, at)
	return err
}

func (t transaction) TouchSession(ctx context.Context, id string, at time.Time) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.auth_sessions SET last_refreshed_at=$2 WHERE id=$1", id, at)
	return err
}

func (t transaction) UpdatePassword(ctx context.Context, id, hash string) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.users SET password_hash=$2,updated_by=$1 WHERE id=$1", id, hash)
	return err
}

func (t transaction) RecordAudit(ctx context.Context, event auth.AuditEvent) error {
	payload := []byte("{}")
	if event.Details != nil {
		encoded, err := json.Marshal(event.Details)
		if err != nil {
			return fmt.Errorf("encode audit details: %w", err)
		}
		payload = encoded
	}
	_, err := t.tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,session_id,request_id,details) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)", event.EventType, event.Outcome, nullable(event.ActorID), nullable(event.SubjectID), nullable(event.EntityID), nullable(event.SessionID), event.RequestID, payload)
	return err
}

func scanUser(row pgx.Row) (auth.UserRecord, error) {
	var user auth.UserRecord
	err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &user.EntityID, &user.Status, &user.AuthVersion)
	return user, mapNotFound(err)
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ErrNotFound
	}
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
