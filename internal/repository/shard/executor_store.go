package shardrepository

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/executor"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExecutorStore is the executor's dedicated shard adapter. It deliberately
// exposes no API command or reference-data methods.
type ExecutorStore struct{ reader *Reader }

func NewExecutor(pool *pgxpool.Pool) *ExecutorStore { return &ExecutorStore{reader: NewReader(pool)} }

const expiryScheduleBatchSize = 200

func (s *ExecutorStore) ClaimBatch(ctx context.Context, claim executor.Claim) (*executor.Batch, error) {
	tx, err := s.reader.beginTenant(ctx, claim.BankID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var batch executor.Batch
	err = tx.QueryRow(ctx, `SELECT id::text,target_status::text,reason,requested_by::text,requester_role,COALESCE(requester_entity_id::text,''),request_id::text,item_count,attempt_count,lease_version
		FROM bank.card_status_batches WHERE entity_id=$1 AND status='queued' AND next_attempt_at<=clock_timestamp()
		ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, claim.BankID).Scan(&batch.ID, &batch.TargetStatus, &batch.Reason, &batch.RequestedBy, &batch.RequesterRole, &batch.RequesterBankID, &batch.RequestID, &batch.ItemCount, &batch.AttemptCount, &batch.LeaseVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("select batch claim: %w", err)
	}
	err = tx.QueryRow(ctx, `UPDATE bank.card_status_batches SET status='processing',lease_owner=$3,lease_expires_at=clock_timestamp()+$4::interval,lease_version=lease_version+1,attempt_count=attempt_count+1,started_at=COALESCE(started_at,clock_timestamp()),updated_at=clock_timestamp()
		WHERE entity_id=$1 AND id=$2 AND status='queued' RETURNING lease_version,attempt_count`, claim.BankID, batch.ID, claim.Owner, claim.LeaseDuration.String()).Scan(&batch.LeaseVersion, &batch.AttemptCount)
	if err != nil {
		return nil, fmt.Errorf("claim batch: %w", err)
	}
	batch.BankID = claim.BankID
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit batch claim: %w", err)
	}
	return &batch, nil
}

func (s *ExecutorStore) RecoverBatches(ctx context.Context, claim executor.Claim) (int, error) {
	tx, err := s.reader.beginTenant(ctx, claim.BankID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `WITH expired AS (
		SELECT id FROM bank.card_status_batches WHERE entity_id=$1 AND status='processing' AND lease_expires_at<=clock_timestamp() FOR UPDATE SKIP LOCKED)
		UPDATE bank.card_status_batches b SET status='queued',lease_owner=NULL,lease_expires_at=NULL,lease_version=lease_version+1,next_attempt_at=clock_timestamp(),updated_at=clock_timestamp()
		FROM expired WHERE b.entity_id=$1 AND b.id=expired.id`, claim.BankID)
	if err != nil {
		return 0, fmt.Errorf("recover batch leases: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *ExecutorStore) ApplyBatch(ctx context.Context, batch executor.Batch, owner string) error {
	tx, err := s.reader.beginTenant(ctx, batch.BankID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = verifyBatchLease(ctx, tx, batch, owner); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT i.id::text,i.card_id::text,i.operation_id::text,c.status::text
		FROM bank.card_status_batch_items i JOIN bank.cards c ON c.entity_id=i.entity_id AND c.id=i.card_id
		WHERE i.entity_id=$1 AND i.batch_id=$2 ORDER BY c.id FOR UPDATE OF c`, batch.BankID, batch.ID)
	if err != nil {
		return fmt.Errorf("lock batch cards: %w", err)
	}
	defer rows.Close()
	type item struct {
		id, cardID, operationID, status string
		ignored                         bool
	}
	items := make([]item, 0)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.id, &value.cardID, &value.operationID, &value.status); err != nil {
			return fmt.Errorf("scan batch card: %w", err)
		}
		valid, ignored := domain.CanTransition(value.status, batch.TargetStatus)
		if !valid {
			return errors.New("invalid batch transition")
		}
		value.ignored = ignored
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterate batch cards: %w", err)
	}
	rows.Close()
	applied, ignored := 0, 0
	for _, item := range items {
		if item.ignored {
			if _, err = tx.Exec(ctx, "UPDATE bank.card_status_batch_items SET outcome='ignored' WHERE entity_id=$1 AND id=$2 AND outcome='pending'", batch.BankID, item.id); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE bank.card_operations SET status='succeeded',executor_identity=$3,started_at=clock_timestamp(),completed_at=clock_timestamp(),updated_by=$4,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='queued'`, batch.BankID, item.operationID, owner, batch.RequestedBy); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,executor_identity,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,$5,'batch.apply','card',$6,'succeeded',$7,'{"ignored":true}')`, batch.BankID, batch.RequestedBy, batch.RequesterRole, nullable(batch.RequesterBankID), owner, item.cardID, batch.RequestID); err != nil {
				return err
			}
			ignored++
			continue
		}
		if _, err = tx.Exec(ctx, `UPDATE bank.cards SET status=$3::bank.card_state,activated_at=CASE WHEN $3::bank.card_state='active' THEN clock_timestamp() ELSE activated_at END,suspended_at=CASE WHEN $3::bank.card_state='suspended' THEN clock_timestamp() ELSE suspended_at END,closed_at=CASE WHEN $3::bank.card_state='closed' THEN clock_timestamp() ELSE closed_at END,version=version+1,updated_by=$4,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2`, batch.BankID, item.cardID, batch.TargetStatus, batch.RequestedBy); err != nil {
			return fmt.Errorf("apply batch card: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE bank.card_operations SET status='succeeded',executor_identity=$3,started_at=clock_timestamp(),completed_at=clock_timestamp(),updated_by=$4,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='queued'`, batch.BankID, item.operationID, owner, batch.RequestedBy); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, batch.BankID, item.cardID, item.operationID, item.status, batch.TargetStatus, batch.Reason, batch.RequestedBy, batch.RequesterRole, nullable(batch.RequesterBankID)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE bank.card_status_batch_items SET outcome='applied',previous_status=$3 WHERE entity_id=$1 AND id=$2 AND outcome='pending'`, batch.BankID, item.id, item.status); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,executor_identity,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,$5,'batch.apply','card',$6,'succeeded',$7,'{}')`, batch.BankID, batch.RequestedBy, batch.RequesterRole, nullable(batch.RequesterBankID), owner, item.cardID, batch.RequestID); err != nil {
			return err
		}
		applied++
	}
	tag, err := tx.Exec(ctx, `UPDATE bank.card_status_batches SET status='succeeded',applied_count=$5,ignored_count=$6,completed_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp()`, batch.BankID, batch.ID, owner, batch.LeaseVersion, applied, ignored)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return executor.ErrStale
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit batch outcome: %w", err)
	}
	return nil
}

func (s *ExecutorStore) RequeueBatch(ctx context.Context, batch executor.Batch, owner, code string, delay time.Duration) error {
	return s.updateBatchLease(ctx, batch, owner, `UPDATE bank.card_status_batches SET status='queued',lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=clock_timestamp()+$6::interval,failure_code=$5,failure_summary=CASE $5 WHEN 'control_unavailable' THEN 'Authorization dependency is temporarily unavailable.' WHEN 'dependency_unavailable' THEN 'A temporary dependency failure occurred.' ELSE 'The batch will be retried.' END,updated_at=clock_timestamp()`, code, delay)
}
func (s *ExecutorStore) FailBatch(ctx context.Context, batch executor.Batch, owner, code string) error {
	tx, err := s.reader.beginTenant(ctx, batch.BankID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = verifyBatchLease(ctx, tx, batch, owner); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE bank.card_status_batch_items i SET outcome=CASE WHEN c.status=$3 THEN 'ignored' ELSE 'not_applied' END,previous_status=NULL,failure_code=CASE WHEN c.status=$3 THEN NULL ELSE $4 END FROM bank.cards c WHERE i.entity_id=$1 AND i.batch_id=$2 AND c.entity_id=i.entity_id AND c.id=i.card_id AND i.outcome='pending'`, batch.BankID, batch.ID, batch.TargetStatus, safeBatchCode(code)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE bank.card_operations o SET status='failed',executor_identity=$3,started_at=clock_timestamp(),completed_at=clock_timestamp(),failure_code=$4,failure_summary='The batch could not be completed.',updated_at=clock_timestamp() FROM bank.card_status_batch_items i WHERE o.entity_id=$1 AND i.entity_id=o.entity_id AND i.batch_id=$2 AND i.operation_id=o.id AND o.status='queued'`, batch.BankID, batch.ID, owner, safeBatchCode(code)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,executor_identity,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,$5,'batch.apply','card_status_batch',$6,'failed',$7,'{}')`, batch.BankID, batch.RequestedBy, batch.RequesterRole, nullable(batch.RequesterBankID), owner, batch.ID, batch.RequestID); err != nil {
		return err
	}
	var ignored int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM bank.card_status_batch_items WHERE entity_id=$1 AND batch_id=$2 AND outcome='ignored'", batch.BankID, batch.ID).Scan(&ignored); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE bank.card_status_batches SET status='failed',applied_count=0,ignored_count=$5,failure_code=$6,failure_summary=CASE $6 WHEN 'authorization_revoked' THEN 'The requester is no longer authorized.' WHEN 'retry_exhausted' THEN 'The retry limit was reached.' WHEN 'invalid_transition' THEN 'One or more card transitions are not allowed.' ELSE 'The batch could not be completed.' END,completed_at=clock_timestamp(),lease_owner=NULL,lease_expires_at=NULL,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp()`, batch.BankID, batch.ID, owner, batch.LeaseVersion, ignored, safeBatchCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return executor.ErrStale
	}
	return tx.Commit(ctx)
}
func (s *ExecutorStore) updateBatchLease(ctx context.Context, batch executor.Batch, owner, sql, code string, delay time.Duration) error {
	tx, err := s.reader.beginTenant(ctx, batch.BankID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, sql+` WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp()`, batch.BankID, batch.ID, owner, batch.LeaseVersion, safeBatchCode(code), delay.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return executor.ErrStale
	}
	return tx.Commit(ctx)
}

func verifyBatchLease(ctx context.Context, tx pgx.Tx, batch executor.Batch, owner string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM bank.card_status_batches WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp() FOR UPDATE)`, batch.BankID, batch.ID, owner, batch.LeaseVersion).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return executor.ErrStale
	}
	return nil
}
func safeBatchCode(code string) string {
	switch code {
	case "authorization_revoked", "retry_exhausted", "invalid_transition", "control_unavailable", "dependency_unavailable":
		return code
	}
	return "dependency_unavailable"
}

func (s *ExecutorStore) EnsureExpiryRun(ctx context.Context, bank string, day time.Time, owner string) error {
	tx, err := s.reader.beginTenant(ctx, bank)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var runID string
	err = tx.QueryRow(ctx, `INSERT INTO bank.card_expiry_runs(entity_id,run_date,executor_identity) VALUES ($1,$2,$3) ON CONFLICT (entity_id,run_date) DO UPDATE SET updated_at=bank.card_expiry_runs.updated_at RETURNING id::text`, bank, day.Format("2006-01-02"), owner).Scan(&runID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text FROM bank.cards c WHERE c.entity_id=$1 AND c.status IN ('pending','issued','active','suspended') AND c.expires_at IS NOT NULL AND c.expires_at < $2::date + INTERVAL '1 day' AND NOT EXISTS (SELECT FROM bank.card_expiry_run_items i WHERE i.entity_id=c.entity_id AND i.expiry_run_id=$3 AND i.card_id=c.id) ORDER BY c.id LIMIT $4 FOR UPDATE OF c SKIP LOCKED`, bank, day.Format("2006-01-02"), runID, expiryScheduleBatchSize)
	if err != nil {
		return err
	}
	defer rows.Close()
	cardIDs := make([]string, 0)
	for rows.Next() {
		var cardID string
		if err = rows.Scan(&cardID); err != nil {
			return err
		}
		cardIDs = append(cardIDs, cardID)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if len(cardIDs) > 0 {
		if _, err = tx.Exec(ctx, "UPDATE bank.card_expiry_runs SET status='processing',completed_at=NULL,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='completed'", bank, runID); err != nil {
			return err
		}
	}
	count := 0
	for _, cardID := range cardIDs {
		var operationID string
		err = tx.QueryRow(ctx, `INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_role,executor_identity,request_id,started_at,created_by,updated_by) VALUES ($1,$2,'expire','queued','scheduled expiry','system',$3,gen_random_uuid(),clock_timestamp(),NULL,NULL) RETURNING id::text`, bank, cardID, owner).Scan(&operationID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO bank.card_expiry_run_items(entity_id,expiry_run_id,card_id,operation_id) VALUES ($1,$2,$3,$4) ON CONFLICT (entity_id,expiry_run_id,card_id) DO NOTHING", bank, runID, cardID, operationID); err != nil {
			return err
		}
		count++
	}
	if _, err = tx.Exec(ctx, "UPDATE bank.card_expiry_runs SET item_count=(SELECT count(*) FROM bank.card_expiry_run_items WHERE entity_id=$1 AND expiry_run_id=$2),updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2", bank, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *ExecutorStore) ClaimExpiryItem(ctx context.Context, claim executor.Claim) (*executor.ExpiryItem, error) {
	tx, err := s.reader.beginTenant(ctx, claim.BankID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var item executor.ExpiryItem
	err = tx.QueryRow(ctx, `SELECT id::text,expiry_run_id::text,card_id::text,operation_id::text,automatic_retry_count FROM bank.card_expiry_run_items WHERE entity_id=$1 AND status='pending' AND next_attempt_at<=clock_timestamp() ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, claim.BankID).Scan(&item.ID, &item.RunID, &item.CardID, &item.OperationID, &item.AutomaticRetry)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `UPDATE bank.card_expiry_run_items SET status='processing',lease_owner=$3,lease_expires_at=clock_timestamp()+$4::interval,lease_version=lease_version+1,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='pending' RETURNING lease_version`, claim.BankID, item.ID, claim.Owner, claim.LeaseDuration.String()).Scan(&item.LeaseVersion)
	if err != nil {
		return nil, err
	}
	item.BankID = claim.BankID
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *ExecutorStore) RecoverExpiryItems(ctx context.Context, claim executor.Claim) (int, error) {
	tx, err := s.reader.beginTenant(ctx, claim.BankID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH expired AS (
		SELECT id FROM bank.card_expiry_run_items WHERE entity_id=$1 AND status='processing' AND lease_expires_at<=clock_timestamp() FOR UPDATE SKIP LOCKED)
		UPDATE bank.card_expiry_run_items i SET status=CASE WHEN i.automatic_retry_count>=3 THEN 'manual_retry_required' ELSE 'pending' END,
		attempt_count=i.attempt_count+1,automatic_retry_count=i.automatic_retry_count+CASE WHEN i.automatic_retry_count>=3 THEN 0 ELSE 1 END,
		lease_owner=NULL,lease_expires_at=NULL,lease_version=lease_version+1,next_attempt_at=clock_timestamp(),failure_code='expiry_failed',updated_at=clock_timestamp()
		FROM expired WHERE i.entity_id=$1 AND i.id=expired.id RETURNING i.expiry_run_id::text`, claim.BankID)
	if err != nil {
		return 0, fmt.Errorf("recover expiry leases: %w", err)
	}
	runs := make([]string, 0)
	for rows.Next() {
		var run string
		if err = rows.Scan(&run); err != nil {
			rows.Close()
			return 0, err
		}
		runs = append(runs, run)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, run := range runs {
		if err = completeExpiryRun(ctx, tx, claim.BankID, run); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(runs), nil
}

func (s *ExecutorStore) ApplyExpiryItem(ctx context.Context, item executor.ExpiryItem, owner string) error {
	tx, err := s.reader.beginTenant(ctx, item.BankID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = verifyExpiryLease(ctx, tx, item, owner); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRow(ctx, "SELECT status::text FROM bank.cards WHERE entity_id=$1 AND id=$2 FOR UPDATE", item.BankID, item.CardID).Scan(&status); err != nil {
		return err
	}
	result := "expired"
	if status == "expired" {
		result = "skipped_already_expired"
	} else {
		_, valid, _ := domain.Transition(status, "expire")
		if !valid {
			return errors.New("invalid expiry transition")
		}
		if _, err = tx.Exec(ctx, "UPDATE bank.cards SET status='expired',expires_at=COALESCE(expires_at,clock_timestamp()),version=version+1,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2", item.BankID, item.CardID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_role,executor_identity) VALUES ($1,$2,$3,$4,'expired','scheduled expiry','system',$5)", item.BankID, item.CardID, item.OperationID, status, owner); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE bank.card_operations SET status='succeeded',executor_identity=$3,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2", item.BankID, item.OperationID, owner); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_role,executor_identity,action,resource_type,resource_id,outcome,request_id) VALUES ($1,'system',$2,'card.expire','card',$3,'succeeded',gen_random_uuid())", item.BankID, owner, item.CardID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE bank.card_expiry_run_items SET status=$5,attempt_count=attempt_count+1,lease_owner=NULL,lease_expires_at=NULL,failure_code=NULL,updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp()`, item.BankID, item.ID, owner, item.LeaseVersion, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return executor.ErrStale
	}
	if err = completeExpiryRun(ctx, tx, item.BankID, item.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *ExecutorStore) RequeueExpiryItem(ctx context.Context, item executor.ExpiryItem, owner string, delay time.Duration) error {
	tx, err := s.reader.beginTenant(ctx, item.BankID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status := "pending"
	if item.AutomaticRetry >= 3 {
		status = "manual_retry_required"
	}
	tag, err := tx.Exec(ctx, `UPDATE bank.card_expiry_run_items SET status=$5,attempt_count=attempt_count+1,automatic_retry_count=automatic_retry_count+CASE WHEN $5='pending' THEN 1 ELSE 0 END,lease_owner=NULL,lease_expires_at=NULL,next_attempt_at=clock_timestamp()+$6::interval,failure_code='expiry_failed',updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp()`, item.BankID, item.ID, owner, item.LeaseVersion, status, delay.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return executor.ErrStale
	}
	if err = completeExpiryRun(ctx, tx, item.BankID, item.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *ExecutorStore) FailExpiryItem(ctx context.Context, item executor.ExpiryItem, owner string) error {
	return s.RequeueExpiryItem(ctx, item, owner, time.Second)
}
func verifyExpiryLease(ctx context.Context, tx pgx.Tx, item executor.ExpiryItem, owner string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM bank.card_expiry_run_items WHERE entity_id=$1 AND id=$2 AND status='processing' AND lease_owner=$3 AND lease_version=$4 AND lease_expires_at>clock_timestamp() FOR UPDATE)`, item.BankID, item.ID, owner, item.LeaseVersion).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return executor.ErrStale
	}
	return nil
}
func completeExpiryRun(ctx context.Context, tx pgx.Tx, bank, run string) error {
	_, err := tx.Exec(ctx, `UPDATE bank.card_expiry_runs SET status='completed',expired_count=(SELECT count(*) FROM bank.card_expiry_run_items WHERE entity_id=$1 AND expiry_run_id=$2 AND status='expired'),skipped_already_expired_count=(SELECT count(*) FROM bank.card_expiry_run_items WHERE entity_id=$1 AND expiry_run_id=$2 AND status='skipped_already_expired'),manual_retry_required_count=(SELECT count(*) FROM bank.card_expiry_run_items WHERE entity_id=$1 AND expiry_run_id=$2 AND status='manual_retry_required'),completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE entity_id=$1 AND id=$2 AND status='processing' AND NOT EXISTS (SELECT FROM bank.card_expiry_run_items WHERE entity_id=$1 AND expiry_run_id=$2 AND status IN ('pending','processing'))`, bank, run)
	return err
}
