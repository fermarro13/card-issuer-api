package controlrepository

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Users(ctx context.Context) ([]domain.DirectoryUser, error) {
	rows, err := s.pool.Query(ctx, "SELECT id::text,normalized_username,role,COALESCE(entity_id::text,''),status,created_at,updated_at FROM control.users ORDER BY created_at DESC,id DESC")
	if err != nil {
		return nil, fmt.Errorf("list staff users: %w", err)
	}
	defer rows.Close()
	users := make([]domain.DirectoryUser, 0)
	for rows.Next() {
		var user domain.DirectoryUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.EntityID, &user.Status, &user.CreatedAt, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan staff user: %w", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staff users: %w", err)
	}
	return users, nil
}

func (s *Store) User(ctx context.Context, id string) (domain.DirectoryUser, error) {
	var user domain.DirectoryUser
	err := s.pool.QueryRow(ctx, "SELECT id::text,normalized_username,role,COALESCE(entity_id::text,''),status,created_at,updated_at FROM control.users WHERE id=$1", id).Scan(&user.ID, &user.Username, &user.Role, &user.EntityID, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return domain.DirectoryUser{}, fmt.Errorf("get staff user: %w", err)
	}
	return user, nil
}

func (s *Store) transaction(ctx context.Context) (transaction, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return transaction{}, fmt.Errorf("begin control transaction: %w", err)
	}
	return transaction{tx: tx}, nil
}

type transaction struct{ tx pgx.Tx }

func (t transaction) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t transaction) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t transaction) ClaimIdempotency(ctx context.Context, claim domain.CentralIdempotencyClaim) (domain.CentralIdempotencyReplay, error) {
	var fingerprint, response []byte
	var expiresAt time.Time
	var status int
	err := t.tx.QueryRow(ctx, "SELECT request_fingerprint,expires_at,response_body,response_status FROM control.command_idempotency_records WHERE operation_scope=$1 AND idempotency_key=$2 FOR UPDATE", claim.Scope, claim.Key).Scan(&fingerprint, &expiresAt, &response, &status)
	if err == nil {
		if claim.Now.Before(expiresAt) {
			if !hmac.Equal(fingerprint, claim.Fingerprint) {
				return domain.CentralIdempotencyReplay{}, domain.ErrIdempotencyConflict
			}
			return domain.CentralIdempotencyReplay{Response: json.RawMessage(response), Found: true}, nil
		}
		_, err = t.tx.Exec(ctx, "UPDATE control.command_idempotency_records SET request_fingerprint=$3,response_status=202,response_body='{}',result_reference=NULL,expires_at=$4 WHERE operation_scope=$1 AND idempotency_key=$2", claim.Scope, claim.Key, claim.Fingerprint, claim.ExpiresAt)
		return domain.CentralIdempotencyReplay{}, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.CentralIdempotencyReplay{}, err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO control.command_idempotency_records(operation_scope,idempotency_key,request_fingerprint,response_status,response_body,expires_at) VALUES ($1,$2,$3,202,'{}',$4)", claim.Scope, claim.Key, claim.Fingerprint, claim.ExpiresAt)
	return domain.CentralIdempotencyReplay{}, err
}

func (t transaction) FinishIdempotency(ctx context.Context, scope, key string, status int, response json.RawMessage, resultID string) error {
	var result any
	if resultID != "" {
		result = resultID
	}
	_, err := t.tx.Exec(ctx, "UPDATE control.command_idempotency_records SET response_status=$3,response_body=$4,result_reference=$5 WHERE operation_scope=$1 AND idempotency_key=$2", scope, key, status, response, result)
	return err
}

func (t transaction) CreateBankRoute(ctx context.Context, entityID, shardID string) error {
	_, err := t.tx.Exec(ctx, "INSERT INTO control.bank_routing_entries(entity_id,shard_id,placement_status) VALUES ($1,$2,'provisioning')", entityID, shardID)
	return err
}

func (t transaction) SetBankRouteStatus(ctx context.Context, entityID, shardID, status string) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.bank_routing_entries SET placement_status=$3,updated_at=clock_timestamp() WHERE entity_id=$1 AND shard_id=$2", entityID, shardID, status)
	return err
}

func (t transaction) SetBankRouteStatusForEntity(ctx context.Context, entityID, status string) error {
	_, err := t.tx.Exec(ctx, "UPDATE control.bank_routing_entries SET placement_status=$2,updated_at=clock_timestamp() WHERE entity_id=$1", entityID, status)
	return err
}

func (t transaction) CreateUser(ctx context.Context, input domain.StaffUserCreate) (domain.DirectoryUser, error) {
	var id string
	err := t.tx.QueryRow(ctx, "INSERT INTO control.users(normalized_username,password_hash,role,entity_id,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$5) RETURNING id::text", input.Username, input.PasswordHash, input.Role, nullableID(input.EntityID), input.ActorID).Scan(&id)
	if err != nil {
		return domain.DirectoryUser{}, err
	}
	return userByID(ctx, t.tx, id)
}

func (t transaction) UpdateUserRole(ctx context.Context, id, role, entityID, actorID string) (domain.DirectoryUser, bool, error) {
	tag, err := t.tx.Exec(ctx, "UPDATE control.users SET role=$2,entity_id=$3,updated_by=$4 WHERE id=$1", id, role, nullableID(entityID), actorID)
	if err != nil || tag.RowsAffected() != 1 {
		return domain.DirectoryUser{}, tag.RowsAffected() == 1, err
	}
	user, err := userByID(ctx, t.tx, id)
	return user, true, err
}

func (t transaction) UpdateUserStatus(ctx context.Context, id, status, actorID string) (domain.DirectoryUser, bool, error) {
	tag, err := t.tx.Exec(ctx, "UPDATE control.users SET status=$2,updated_by=$3 WHERE id=$1", id, status, actorID)
	if err != nil || tag.RowsAffected() != 1 {
		return domain.DirectoryUser{}, tag.RowsAffected() == 1, err
	}
	user, err := userByID(ctx, t.tx, id)
	return user, true, err
}

func (t transaction) UpdateUserPassword(ctx context.Context, id, passwordHash, actorID string) (domain.DirectoryUser, bool, error) {
	tag, err := t.tx.Exec(ctx, "UPDATE control.users SET password_hash=$2,updated_by=$3 WHERE id=$1", id, passwordHash, actorID)
	if err != nil || tag.RowsAffected() != 1 {
		return domain.DirectoryUser{}, tag.RowsAffected() == 1, err
	}
	user, err := userByID(ctx, t.tx, id)
	return user, true, err
}

func (t transaction) RecordAuthenticationAudit(ctx context.Context, event domain.AuthenticationAudit) error {
	_, err := t.tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,request_id) VALUES ($1,'succeeded',$2,$3,$4,$5)", event.EventType, event.ActorUserID, event.SubjectUserID, nullableID(event.EntityID), event.RequestID)
	return err
}

func userByID(ctx context.Context, tx pgx.Tx, id string) (domain.DirectoryUser, error) {
	var user domain.DirectoryUser
	err := tx.QueryRow(ctx, "SELECT id::text,normalized_username,role,COALESCE(entity_id::text,''),status,created_at,updated_at FROM control.users WHERE id=$1", id).Scan(&user.ID, &user.Username, &user.Role, &user.EntityID, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func nullableID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
