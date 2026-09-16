package shardrepository

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"time"

	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5"
)

type transaction struct{ tx pgx.Tx }

func (t transaction) Commit(ctx context.Context) error   { return t.tx.Commit(ctx) }
func (t transaction) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }

func (t transaction) ClaimIdempotency(ctx context.Context, claim domain.ShardIdempotencyClaim) (domain.ShardIdempotencyReplay, error) {
	load := func() (domain.ShardIdempotencyReplay, error) {
		var id string
		var fingerprint, response []byte
		var expiresAt time.Time
		var status *int
		err := t.tx.QueryRow(ctx, "SELECT id::text,request_fingerprint,expires_at,response_body,response_status FROM bank.idempotency_records WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3 FOR UPDATE", claim.Bank, claim.Scope, claim.Key).Scan(&id, &fingerprint, &expiresAt, &response, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ShardIdempotencyReplay{}, nil
		}
		if err != nil {
			return domain.ShardIdempotencyReplay{}, err
		}
		if claim.Now.Before(expiresAt) {
			if !hmac.Equal(fingerprint, claim.Fingerprint) {
				return domain.ShardIdempotencyReplay{}, domain.ErrIdempotencyConflict
			}
			if status == nil {
				return domain.ShardIdempotencyReplay{}, errors.New("idempotency response status is missing")
			}
			return domain.ShardIdempotencyReplay{RecordID: id, Response: json.RawMessage(response), Status: *status, Found: true}, nil
		}
		err = t.tx.QueryRow(ctx, "UPDATE bank.idempotency_records SET request_fingerprint=$4,response_status=202,response_body='{\"status\":\"processing\"}',result_reference=NULL,status='processing',expires_at=$5,updated_by=$6 WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3 RETURNING id::text", claim.Bank, claim.Scope, claim.Key, claim.Fingerprint, claim.ExpiresAt, "00000000-0000-4000-8000-000000000000").Scan(&id)
		return domain.ShardIdempotencyReplay{RecordID: id}, err
	}

	replay, err := load()
	if err != nil || replay.Found || replay.RecordID != "" {
		return replay, err
	}

	var id string
	err = t.tx.QueryRow(ctx, "INSERT INTO bank.idempotency_records(entity_id,operation_scope,idempotency_key,request_fingerprint,response_status,response_body,expires_at,created_by,updated_by) VALUES ($1,$2,$3,$4,202,'{\"status\":\"processing\"}',$5,$6,$6) ON CONFLICT (entity_id,operation_scope,idempotency_key) DO NOTHING RETURNING id::text", claim.Bank, claim.Scope, claim.Key, claim.Fingerprint, claim.ExpiresAt, "00000000-0000-4000-8000-000000000000").Scan(&id)
	if err == nil {
		return domain.ShardIdempotencyReplay{RecordID: id}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ShardIdempotencyReplay{}, err
	}
	return load()
}

func (t transaction) FinishIdempotency(ctx context.Context, bank, scope, key string, status int, response json.RawMessage, result, actor string) error {
	var resultID any
	if result != "" {
		resultID = result
	}
	_, err := t.tx.Exec(ctx, "UPDATE bank.idempotency_records SET status='succeeded',response_status=$4,response_body=$5,result_reference=$6,updated_by=$7 WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3", bank, scope, key, status, response, resultID, actor)
	return err
}
