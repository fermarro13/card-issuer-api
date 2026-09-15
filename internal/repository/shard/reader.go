package shardrepository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domain "card-issuer-api/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Reader struct{ pool *pgxpool.Pool }

func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

func (r *Reader) Entity(ctx context.Context, id string) (domain.Entity, error) {
	tx, err := r.beginTenant(ctx, id)
	if err != nil {
		return domain.Entity{}, err
	}
	defer tx.Rollback(ctx)
	entity, err := scanEntity(tx.QueryRow(ctx, "SELECT id::text,bank_reference,name,status,created_at,updated_at FROM bank.entities WHERE id=$1", id))
	if err != nil {
		return domain.Entity{}, fmt.Errorf("get bank entity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Entity{}, fmt.Errorf("commit bank entity read: %w", err)
	}
	return entity, nil
}

func (r *Reader) Card(ctx context.Context, bank, id string) (domain.Card, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return domain.Card{}, err
	}
	defer tx.Rollback(ctx)
	card, err := scanCard(tx.QueryRow(ctx, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status::text,masked_pan,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND id=$2", bank, id))
	if err != nil {
		return domain.Card{}, fmt.Errorf("get card: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Card{}, fmt.Errorf("commit card read: %w", err)
	}
	return card, nil
}

func (r *Reader) Cards(ctx context.Context, bank string, filter domain.CardFilter) ([]domain.Card, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status::text,masked_pan,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND ($2::uuid IS NULL OR client_id=$2) AND ($3::uuid IS NULL OR account_reference_id=$3) AND ($4::uuid IS NULL OR product_id=$4) AND ($5::bank.card_state IS NULL OR status=$5) ORDER BY created_at DESC,id DESC", bank, nullable(filter.ClientID), nullable(filter.AccountID), nullable(filter.ProductID), nullable(filter.Status))
	if err != nil {
		return nil, fmt.Errorf("list cards: %w", err)
	}
	defer rows.Close()
	cards := make([]domain.Card, 0)
	for rows.Next() {
		card, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cards: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card list: %w", err)
	}
	return cards, nil
}

func (r *Reader) CardOperations(ctx context.Context, bank, cardID string) ([]domain.CardOperation, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,card_id::text,action,status,reason,created_at,completed_at FROM bank.card_operations WHERE entity_id=$1 AND card_id=$2 ORDER BY created_at DESC,id DESC", bank, cardID)
	if err != nil {
		return nil, fmt.Errorf("list card operations: %w", err)
	}
	defer rows.Close()
	operations := make([]domain.CardOperation, 0)
	for rows.Next() {
		operation, err := scanCardOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card operation: %w", err)
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card operations: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card operation list: %w", err)
	}
	return operations, nil
}

func (r *Reader) CardStatusHistory(ctx context.Context, bank, cardID string) ([]domain.CardStatusHistory, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,previous_status::text,new_status::text,reason,created_at FROM bank.card_status_history WHERE entity_id=$1 AND card_id=$2 ORDER BY created_at DESC,id DESC", bank, cardID)
	if err != nil {
		return nil, fmt.Errorf("list card status history: %w", err)
	}
	defer rows.Close()
	history := make([]domain.CardStatusHistory, 0)
	for rows.Next() {
		item, err := scanCardStatusHistory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card status history: %w", err)
		}
		history = append(history, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card status history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card status history list: %w", err)
	}
	return history, nil
}

func (r *Reader) CardStatusBatches(ctx context.Context, bank string) ([]domain.CardStatusBatch, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at FROM bank.card_status_batches WHERE entity_id=$1 ORDER BY created_at DESC,id DESC", bank)
	if err != nil {
		return nil, fmt.Errorf("list card status batches: %w", err)
	}
	defer rows.Close()
	batches := make([]domain.CardStatusBatch, 0)
	for rows.Next() {
		batch, err := scanCardStatusBatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card status batch: %w", err)
		}
		batches = append(batches, batch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card status batches: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card status batch list: %w", err)
	}
	return batches, nil
}

func (r *Reader) CardStatusBatch(ctx context.Context, bank, id string) (domain.CardStatusBatch, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return domain.CardStatusBatch{}, err
	}
	defer tx.Rollback(ctx)
	batch, err := scanCardStatusBatch(tx.QueryRow(ctx, "SELECT id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at FROM bank.card_status_batches WHERE entity_id=$1 AND id=$2", bank, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CardStatusBatch{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.CardStatusBatch{}, fmt.Errorf("get card status batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CardStatusBatch{}, fmt.Errorf("commit card status batch read: %w", err)
	}
	return batch, nil
}

func (r *Reader) CardStatusBatchItems(ctx context.Context, bank, batchID string) ([]domain.CardStatusBatchItem, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM bank.card_status_batches WHERE entity_id=$1 AND id=$2)", bank, batchID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check card status batch: %w", err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}
	rows, err := tx.Query(ctx, "SELECT i.id::text,i.card_id::text,i.operation_id::text,i.previous_status::text,i.outcome,i.failure_code,b.created_at FROM bank.card_status_batch_items i JOIN bank.card_status_batches b ON b.entity_id=i.entity_id AND b.id=i.batch_id WHERE i.entity_id=$1 AND i.batch_id=$2 ORDER BY b.created_at DESC,i.id DESC", bank, batchID)
	if err != nil {
		return nil, fmt.Errorf("list card status batch items: %w", err)
	}
	defer rows.Close()
	items := make([]domain.CardStatusBatchItem, 0)
	for rows.Next() {
		item, err := scanCardStatusBatchItem(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card status batch item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card status batch items: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card status batch item list: %w", err)
	}
	return items, nil
}

func (r *Reader) CardProducts(ctx context.Context, bank string) ([]domain.CardProduct, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,product_code,name,status,configuration,configuration_version,created_at,updated_at FROM bank.card_products WHERE entity_id=$1 ORDER BY created_at DESC,id DESC", bank)
	if err != nil {
		return nil, fmt.Errorf("list card products: %w", err)
	}
	defer rows.Close()
	products := make([]domain.CardProduct, 0)
	for rows.Next() {
		product, err := scanCardProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card product: %w", err)
		}
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate card products: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit card product list: %w", err)
	}
	return products, nil
}

func (r *Reader) CardProduct(ctx context.Context, bank, id string) (domain.CardProduct, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return domain.CardProduct{}, err
	}
	defer tx.Rollback(ctx)
	product, err := scanCardProduct(tx.QueryRow(ctx, "SELECT id::text,product_code,name,status,configuration,configuration_version,created_at,updated_at FROM bank.card_products WHERE entity_id=$1 AND id=$2", bank, id))
	if err != nil {
		return domain.CardProduct{}, fmt.Errorf("get card product: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CardProduct{}, fmt.Errorf("commit card product read: %w", err)
	}
	return product, nil
}

func (r *Reader) Clients(ctx context.Context, bank string) ([]domain.Client, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,external_client_ref,display_name,created_at,updated_at FROM bank.clients WHERE entity_id=$1 ORDER BY created_at DESC,id DESC", bank)
	if err != nil {
		return nil, fmt.Errorf("list clients: %w", err)
	}
	defer rows.Close()
	clients := make([]domain.Client, 0)
	for rows.Next() {
		client, err := scanClient(rows)
		if err != nil {
			return nil, fmt.Errorf("scan client: %w", err)
		}
		clients = append(clients, client)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate clients: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit client list: %w", err)
	}
	return clients, nil
}

func (r *Reader) Client(ctx context.Context, bank, id string) (domain.Client, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return domain.Client{}, err
	}
	defer tx.Rollback(ctx)
	client, err := scanClient(tx.QueryRow(ctx, "SELECT id::text,external_client_ref,display_name,created_at,updated_at FROM bank.clients WHERE entity_id=$1 AND id=$2", bank, id))
	if err != nil {
		return domain.Client{}, fmt.Errorf("get client: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Client{}, fmt.Errorf("commit client read: %w", err)
	}
	return client, nil
}

func (r *Reader) AccountReferences(ctx context.Context, bank string) ([]domain.AccountReference, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT id::text,client_id::text,external_account_ref,created_at,updated_at FROM bank.account_references WHERE entity_id=$1 ORDER BY created_at DESC,id DESC", bank)
	if err != nil {
		return nil, fmt.Errorf("list account references: %w", err)
	}
	defer rows.Close()
	accounts := make([]domain.AccountReference, 0)
	for rows.Next() {
		account, err := scanAccountReference(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account reference: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate account references: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit account reference list: %w", err)
	}
	return accounts, nil
}

func (r *Reader) AccountReference(ctx context.Context, bank, id string) (domain.AccountReference, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return domain.AccountReference{}, err
	}
	defer tx.Rollback(ctx)
	account, err := scanAccountReference(tx.QueryRow(ctx, "SELECT id::text,client_id::text,external_account_ref,created_at,updated_at FROM bank.account_references WHERE entity_id=$1 AND id=$2", bank, id))
	if err != nil {
		return domain.AccountReference{}, fmt.Errorf("get account reference: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AccountReference{}, fmt.Errorf("commit account reference read: %w", err)
	}
	return account, nil
}

type cardRow interface{ Scan(...any) error }

func scanEntity(row cardRow) (domain.Entity, error) {
	var entity domain.Entity
	err := row.Scan(&entity.ID, &entity.BankReference, &entity.Name, &entity.Status, &entity.CreatedAt, &entity.UpdatedAt)
	return entity, err
}

func scanCard(row cardRow) (domain.Card, error) {
	var card domain.Card
	err := row.Scan(
		&card.ID, &card.ClientID, &card.AccountReferenceID, &card.ProductID, &card.Status, &card.MaskedPAN,
		&card.PredecessorCardID, &card.IssuedAt, &card.ActivatedAt, &card.SuspendedAt,
		&card.ClosedAt, &card.ExpiresAt, &card.Version, &card.CreatedAt, &card.UpdatedAt,
	)
	return card, err
}

func scanCardProduct(row cardRow) (domain.CardProduct, error) {
	var product domain.CardProduct
	var configuration []byte
	err := row.Scan(&product.ID, &product.ProductCode, &product.Name, &product.Status, &configuration, &product.ConfigurationVersion, &product.CreatedAt, &product.UpdatedAt)
	product.Configuration = json.RawMessage(configuration)
	return product, err
}

func scanClient(row cardRow) (domain.Client, error) {
	var client domain.Client
	err := row.Scan(&client.ID, &client.ExternalClientRef, &client.DisplayName, &client.CreatedAt, &client.UpdatedAt)
	return client, err
}

func scanAccountReference(row cardRow) (domain.AccountReference, error) {
	var account domain.AccountReference
	err := row.Scan(&account.ID, &account.ClientID, &account.ExternalAccountRef, &account.CreatedAt, &account.UpdatedAt)
	return account, err
}

func scanCardOperation(row cardRow) (domain.CardOperation, error) {
	var operation domain.CardOperation
	err := row.Scan(&operation.ID, &operation.CardID, &operation.Action, &operation.Status, &operation.Reason, &operation.CreatedAt, &operation.CompletedAt)
	return operation, err
}

func scanCardStatusHistory(row cardRow) (domain.CardStatusHistory, error) {
	var item domain.CardStatusHistory
	err := row.Scan(&item.ID, &item.PreviousStatus, &item.NewStatus, &item.Reason, &item.CreatedAt)
	return item, err
}

func scanCardStatusBatch(row cardRow) (domain.CardStatusBatch, error) {
	var batch domain.CardStatusBatch
	err := row.Scan(
		&batch.ID,
		&batch.TargetStatus,
		&batch.Reason,
		&batch.CreatedBy,
		&batch.RequesterRole,
		&batch.Status,
		&batch.ItemCount,
		&batch.AppliedCount,
		&batch.IgnoredCount,
		&batch.FailureCode,
		&batch.FailureSummary,
		&batch.RetryOfBatchID,
		&batch.CreatedAt,
		&batch.UpdatedAt,
		&batch.CompletedAt,
	)
	return batch, err
}

func scanCardStatusBatchItem(row cardRow) (domain.CardStatusBatchItem, error) {
	var item domain.CardStatusBatchItem
	err := row.Scan(&item.ID, &item.CardID, &item.OperationID, &item.PreviousStatus, &item.Outcome, &item.FailureCode, &item.CreatedAt)
	return item, err
}

func (r *Reader) beginTenant(ctx context.Context, bank string) (pgx.Tx, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin shard transaction: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.entity_id',$1,true)", bank); err != nil {
		tx.Rollback(ctx)
		return nil, fmt.Errorf("set shard tenant context: %w", err)
	}
	return tx, nil
}

func (r *Reader) transaction(ctx context.Context, bank string) (transaction, error) {
	tx, err := r.beginTenant(ctx, bank)
	if err != nil {
		return transaction{}, err
	}
	return transaction{tx: tx}, nil
}

func (t transaction) ProvisionEntity(ctx context.Context, input domain.EntityProvision) error {
	if _, err := t.tx.Exec(ctx, "INSERT INTO bank.entities(id,bank_reference,name,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,updated_by=EXCLUDED.updated_by", input.ID, input.BankReference, input.Name, input.ActorID); err != nil {
		return err
	}
	_, err := t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator','bank_provision','bank',$1,'succeeded',$3)", input.ID, input.ActorID, input.RequestID)
	return err
}

func (t transaction) UpdateEntity(ctx context.Context, input domain.EntityPatch) (domain.Entity, error) {
	entity, err := scanEntity(t.tx.QueryRow(ctx, "UPDATE bank.entities SET name=COALESCE($2,name),status=COALESCE($3,status),updated_by=$4 WHERE id=$1 RETURNING id::text,bank_reference,name,status,created_at,updated_at", input.ID, input.Name, input.Status, input.ActorID))
	if err != nil {
		return domain.Entity{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator','bank_update','bank',$1,'succeeded',$3)", input.ID, input.ActorID, input.RequestID); err != nil {
		return domain.Entity{}, err
	}
	return entity, nil
}

func (t transaction) CreateCardProduct(ctx context.Context, bank string, input domain.CardProductCreate) (domain.CardProduct, error) {
	product, err := scanCardProduct(t.tx.QueryRow(ctx, "INSERT INTO bank.card_products(entity_id,product_code,name,status,configuration,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$6,$6) RETURNING id::text,product_code,name,status,configuration,configuration_version,created_at,updated_at", bank, input.ProductCode, input.Name, input.Status, input.Configuration, input.ActorID))
	return product, err
}

func (t transaction) UpdateCardProduct(ctx context.Context, bank, id string, input domain.CardProductPatch) (domain.CardProduct, bool, error) {
	product, err := scanCardProduct(t.tx.QueryRow(ctx, "UPDATE bank.card_products SET name=COALESCE($3,name),status=COALESCE($4,status),configuration=COALESCE($5,configuration),configuration_version=configuration_version+CASE WHEN $5::jsonb IS NULL OR configuration=$5::jsonb THEN 0 ELSE 1 END,updated_by=$6 WHERE entity_id=$1 AND id=$2 RETURNING id::text,product_code,name,status,configuration,configuration_version,created_at,updated_at", bank, id, input.Name, input.Status, input.Configuration, input.ActorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CardProduct{}, false, nil
	}
	return product, err == nil, err
}

func (t transaction) CreateClient(ctx context.Context, bank string, input domain.ClientCreate) (domain.Client, error) {
	client, err := scanClient(t.tx.QueryRow(ctx, "INSERT INTO bank.clients(entity_id,external_client_ref,display_name,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) RETURNING id::text,external_client_ref,display_name,created_at,updated_at", bank, input.ExternalClientRef, input.DisplayName, input.ActorID))
	return client, err
}

func (t transaction) UpdateClient(ctx context.Context, bank, id string, input domain.ClientPatch) (domain.Client, bool, error) {
	client, err := scanClient(t.tx.QueryRow(ctx, "UPDATE bank.clients SET display_name=$3,updated_by=$4 WHERE entity_id=$1 AND id=$2 RETURNING id::text,external_client_ref,display_name,created_at,updated_at", bank, id, input.DisplayName, input.ActorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Client{}, false, nil
	}
	return client, err == nil, err
}

func (t transaction) CreateAccountReference(ctx context.Context, bank string, input domain.AccountReferenceCreate) (domain.AccountReference, error) {
	account, err := scanAccountReference(t.tx.QueryRow(ctx, "INSERT INTO bank.account_references(entity_id,client_id,external_account_ref,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) RETURNING id::text,client_id::text,external_account_ref,created_at,updated_at", bank, input.ClientID, input.ExternalAccountRef, input.ActorID))
	return account, err
}

func (t transaction) UpdateAccountReference(ctx context.Context, bank, id string, input domain.AccountReferencePatch) (domain.AccountReference, bool, error) {
	account, err := scanAccountReference(t.tx.QueryRow(ctx, "UPDATE bank.account_references SET client_id=$3,updated_by=$4 WHERE entity_id=$1 AND id=$2 RETURNING id::text,client_id::text,external_account_ref,created_at,updated_at", bank, id, input.ClientID, input.ActorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AccountReference{}, false, nil
	}
	return account, err == nil, err
}

func (t transaction) RecordReferenceAudit(ctx context.Context, audit domain.ReferenceAudit) error {
	_, err := t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,$5,$6,$7,'succeeded',$8)", audit.Bank, audit.ActorID, audit.ActorRole, nullable(audit.ActorEntityID), audit.Action, audit.Kind, audit.ResourceID, audit.RequestID)
	return err
}

func (t transaction) IssueReferencesValid(ctx context.Context, bank, clientID, accountID, productID string) (bool, error) {
	var valid bool
	err := t.tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM bank.account_references a JOIN bank.card_products p ON p.entity_id=a.entity_id WHERE a.entity_id=$1 AND a.id=$2 AND a.client_id=$3 AND p.id=$4 AND p.status='active')", bank, accountID, clientID, productID).Scan(&valid)
	return valid, err
}

func (t transaction) IssueCard(ctx context.Context, bank string, input domain.CardIssue) (domain.Card, domain.CardOperation, error) {
	card, err := scanCard(t.tx.QueryRow(ctx, "INSERT INTO bank.cards(entity_id,id,client_id,account_reference_id,product_id,status,masked_pan,issued_at,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,'issued',$6,clock_timestamp(),$7,$7) RETURNING id::text,client_id::text,account_reference_id::text,product_id::text,status::text,masked_pan,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at", bank, input.CardID, input.ClientID, input.AccountReferenceID, input.ProductID, input.MaskedPAN, input.ActorID))
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	operation, err := scanCardOperation(t.tx.QueryRow(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,'issue','succeeded',$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp(),$5,$5) RETURNING id::text,card_id::text,action,status,reason,created_at,completed_at", bank, input.OperationID, input.CardID, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.RequestID))
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,'pending','issued',$4,$5,$6,$7)", bank, input.CardID, input.OperationID, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID)); err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card.issue','card',$5,'succeeded',$6)", bank, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.CardID, input.RequestID); err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	return card, operation, nil
}

func (t transaction) LockCard(ctx context.Context, bank, id string) (domain.CardState, error) {
	var state domain.CardState
	err := t.tx.QueryRow(ctx, "SELECT status::text,client_id::text,account_reference_id::text,product_id::text FROM bank.cards WHERE entity_id=$1 AND id=$2 FOR UPDATE", bank, id).Scan(&state.Status, &state.ClientID, &state.AccountReferenceID, &state.ProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CardState{}, domain.ErrNotFound
	}
	return state, err
}

func (t transaction) ReplacementPending(ctx context.Context, bank, predecessorID string) (bool, error) {
	var pending bool
	err := t.tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM bank.cards WHERE entity_id=$1 AND predecessor_card_id=$2 AND status='issued')", bank, predecessorID).Scan(&pending)
	return pending, err
}

func (t transaction) ReplaceCard(ctx context.Context, bank string, input domain.CardReplacement) (domain.Card, domain.CardOperation, error) {
	card, err := scanCard(t.tx.QueryRow(ctx, "INSERT INTO bank.cards(entity_id,id,client_id,account_reference_id,product_id,status,masked_pan,predecessor_card_id,issued_at,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,'issued',$6,$7,clock_timestamp(),$8,$8) RETURNING id::text,client_id::text,account_reference_id::text,product_id::text,status::text,masked_pan,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at", bank, input.SuccessorID, input.ClientID, input.AccountReferenceID, input.ProductID, input.MaskedPAN, input.PredecessorID, input.ActorID))
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	operation, err := t.insertCardOperation(ctx, bank, input.OperationID, input.SuccessorID, "replace", input.Reason, input.ActorID, input.ActorRole, input.ActorEntityID, input.RequestID)
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,'pending','issued',$4,$5,$6,$7)", bank, input.SuccessorID, input.OperationID, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID)); err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card.replace','card',$5,'succeeded',$6)", bank, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.SuccessorID, input.RequestID); err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	return card, operation, nil
}

func (t transaction) ApplyCardCommand(ctx context.Context, bank string, input domain.CardCommand) (domain.Card, domain.CardOperation, error) {
	operation, err := t.insertCardOperation(ctx, bank, input.OperationID, input.CardID, input.Action, input.Reason, input.ActorID, input.ActorRole, input.ActorEntityID, input.RequestID)
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	if !input.Ignored {
		if err = t.updateCardStatus(ctx, input.Action, bank, input.CardID, input.TargetStatus, input.ActorID); err != nil {
			return domain.Card{}, domain.CardOperation{}, err
		}
		if _, err = t.tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)", bank, input.CardID, input.OperationID, input.PreviousStatus, input.TargetStatus, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID)); err != nil {
			return domain.Card{}, domain.CardOperation{}, err
		}
		if input.Action == "activate" {
			if err = t.closePredecessorOnActivation(ctx, bank, input); err != nil {
				return domain.Card{}, domain.CardOperation{}, err
			}
		}
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,$5,'card',$6,'succeeded',$7,$8)", bank, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), "card."+input.Action, input.CardID, input.RequestID, []byte(fmt.Sprintf(`{"ignored":%t}`, input.Ignored))); err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	card, err := scanCard(t.tx.QueryRow(ctx, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status::text,masked_pan,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND id=$2", bank, input.CardID))
	if err != nil {
		return domain.Card{}, domain.CardOperation{}, err
	}
	return card, operation, nil
}

func (t transaction) closePredecessorOnActivation(ctx context.Context, bank string, input domain.CardCommand) error {
	var predecessorID, predecessorStatus string
	err := t.tx.QueryRow(ctx, "SELECT p.id::text,p.status::text FROM bank.cards successor JOIN bank.cards p ON p.entity_id=successor.entity_id AND p.id=successor.predecessor_card_id WHERE successor.entity_id=$1 AND successor.id=$2 FOR UPDATE", bank, input.CardID).Scan(&predecessorID, &predecessorStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || (predecessorStatus != "active" && predecessorStatus != "suspended") {
		return err
	}
	if _, err = t.tx.Exec(ctx, "UPDATE bank.cards SET status='closed',closed_at=clock_timestamp(),version=version+1,updated_by=$3 WHERE entity_id=$1 AND id=$2 AND status IN ('active','suspended')", bank, predecessorID, input.ActorID); err != nil {
		return err
	}
	if _, err = t.insertCardOperation(ctx, bank, input.PredecessorOperationID, predecessorID, "close", input.Reason, input.ActorID, input.ActorRole, input.ActorEntityID, input.RequestID); err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,$4,'closed',$5,$6,$7,$8)", bank, predecessorID, input.PredecessorOperationID, predecessorStatus, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID))
	return err
}

func (t transaction) insertCardOperation(ctx context.Context, bank, operationID, cardID, action, reason, actorID, actorRole, actorEntityID, requestID string) (domain.CardOperation, error) {
	return scanCardOperation(t.tx.QueryRow(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,$4,'succeeded',$5,$6,$7,$8,$9,clock_timestamp(),clock_timestamp(),$6,$6) RETURNING id::text,card_id::text,action,status,reason,created_at,completed_at", bank, operationID, cardID, action, reason, actorID, actorRole, nullable(actorEntityID), requestID))
}

func (t transaction) updateCardStatus(ctx context.Context, action, bank, id, status, actor string) error {
	var err error
	switch action {
	case "activate":
		_, err = t.tx.Exec(ctx, "UPDATE bank.cards SET status=$3,activated_at=clock_timestamp(),version=version+1,updated_by=$4 WHERE entity_id=$1 AND id=$2", bank, id, status, actor)
	case "suspend":
		_, err = t.tx.Exec(ctx, "UPDATE bank.cards SET status=$3,suspended_at=clock_timestamp(),version=version+1,updated_by=$4 WHERE entity_id=$1 AND id=$2", bank, id, status, actor)
	case "close":
		_, err = t.tx.Exec(ctx, "UPDATE bank.cards SET status=$3,closed_at=clock_timestamp(),version=version+1,updated_by=$4 WHERE entity_id=$1 AND id=$2", bank, id, status, actor)
	case "resume":
		_, err = t.tx.Exec(ctx, "UPDATE bank.cards SET status=$3,version=version+1,updated_by=$4 WHERE entity_id=$1 AND id=$2", bank, id, status, actor)
	default:
		return fmt.Errorf("unsupported card transition action %q", action)
	}
	return err
}

func (t transaction) RetryExpiryItem(ctx context.Context, bank, run, item, actor string) (bool, error) {
	_, err := t.tx.Exec(ctx, "UPDATE bank.card_expiry_runs r SET status='processing',completed_at=NULL,updated_at=clock_timestamp() WHERE r.entity_id=$1 AND r.id=$2 AND r.status='completed' AND EXISTS (SELECT FROM bank.card_expiry_run_items i WHERE i.entity_id=r.entity_id AND i.expiry_run_id=r.id AND i.id=$3 AND i.status='manual_retry_required')", bank, run, item)
	if err != nil {
		return false, err
	}
	tag, err := t.tx.Exec(ctx, "UPDATE bank.card_expiry_run_items i SET status='pending',manual_retry_count=manual_retry_count+1,manual_retry_by=$4,manual_retry_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE i.entity_id=$1 AND i.expiry_run_id=$2 AND i.id=$3 AND i.status='manual_retry_required' AND EXISTS (SELECT FROM bank.cards c WHERE c.entity_id=i.entity_id AND c.id=i.card_id AND c.status<>'expired')", bank, run, item, actor)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (t transaction) RecordExpiryRetryAudit(ctx context.Context, bank, item, actor, requestID string) error {
	_, err := t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,'issuer_operator','expiry_retry','expiry_item',$3,'succeeded',$4,$5)", bank, actor, item, requestID, []byte(`{"reason":"manual_retry"}`))
	return err
}

func (t transaction) CardsExist(ctx context.Context, bank string, cardIDs []string) (bool, error) {
	var exists bool
	err := t.tx.QueryRow(ctx, "SELECT count(*) = cardinality($2::uuid[]) FROM bank.cards WHERE entity_id=$1 AND id=ANY($2::uuid[])", bank, cardIDs).Scan(&exists)
	return exists, err
}

func (t transaction) CreateCardStatusBatchDraft(ctx context.Context, bank string, input domain.CardStatusBatchDraft) (domain.CardStatusBatch, []domain.CardStatusBatchItem, error) {
	batch, err := scanCardStatusBatch(t.tx.QueryRow(ctx, "INSERT INTO bank.card_status_batches(entity_id,id,target_status,reason,requested_by,requester_role,requester_entity_id,request_id,idempotency_record_id,item_count,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$5,$5) RETURNING id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at", bank, input.ID, input.TargetStatus, input.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.RequestID, input.IdempotencyRecordID, len(input.CardIDs)))
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	items, err := t.insertCardStatusBatchItems(ctx, bank, input.ID, input.TargetStatus, input.Reason, input.CardIDs, input.OperationIDs, input.ActorID, input.ActorRole, input.ActorEntityID, input.RequestID, batch.CreatedAt)
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	details, err := json.Marshal(map[string]any{"target_status": input.TargetStatus, "item_count": len(input.CardIDs)})
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,'card_status_batch.create','card_status_batch',$5,'succeeded',$6,$7)", bank, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.ID, input.RequestID, details); err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	return batch, items, nil
}

func (t transaction) LockCardStatusBatch(ctx context.Context, bank, id string) (domain.CardStatusBatch, error) {
	batch, err := scanCardStatusBatch(t.tx.QueryRow(ctx, "SELECT id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at FROM bank.card_status_batches WHERE entity_id=$1 AND id=$2 FOR UPDATE", bank, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CardStatusBatch{}, domain.ErrNotFound
	}
	return batch, err
}

func (t transaction) ExecuteCardStatusBatch(ctx context.Context, bank, id, actor, role, actorEntityID, requestID string) (domain.CardStatusBatch, error) {
	batch, err := t.LockCardStatusBatch(ctx, bank, id)
	if err != nil || batch.Status != "draft" {
		return batch, err
	}
	batch, err = scanCardStatusBatch(t.tx.QueryRow(ctx, "UPDATE bank.card_status_batches SET status='queued',next_attempt_at=clock_timestamp(),updated_by=$3 WHERE entity_id=$1 AND id=$2 RETURNING id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at", bank, id, actor))
	if err != nil {
		return domain.CardStatusBatch{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card_status_batch.execute','card_status_batch',$5,'succeeded',$6)", bank, actor, role, nullable(actorEntityID), id, requestID); err != nil {
		return domain.CardStatusBatch{}, err
	}
	return batch, nil
}

func (t transaction) CancelCardStatusBatch(ctx context.Context, bank, id, actor, role, actorEntityID, requestID string) (domain.CardStatusBatch, error) {
	batch, err := t.LockCardStatusBatch(ctx, bank, id)
	if err != nil {
		return domain.CardStatusBatch{}, err
	}
	if batch.Status != "draft" && batch.Status != "queued" {
		return domain.CardStatusBatch{}, domain.ErrBatchNotCancellable
	}
	batch, err = scanCardStatusBatch(t.tx.QueryRow(ctx, "UPDATE bank.card_status_batches SET status='cancelled',completed_at=clock_timestamp(),updated_by=$3 WHERE entity_id=$1 AND id=$2 RETURNING id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at", bank, id, actor))
	if err != nil {
		return domain.CardStatusBatch{}, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card_status_batch.cancel','card_status_batch',$5,'succeeded',$6)", bank, actor, role, nullable(actorEntityID), id, requestID); err != nil {
		return domain.CardStatusBatch{}, err
	}
	return batch, nil
}

func (t transaction) RetryCardStatusBatch(ctx context.Context, bank, sourceID string, input domain.CardStatusBatchRetry) (domain.CardStatusBatch, []domain.CardStatusBatchItem, error) {
	source, err := t.LockCardStatusBatch(ctx, bank, sourceID)
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	if source.Status != "failed" && source.Status != "cancelled" {
		return domain.CardStatusBatch{}, nil, domain.ErrBatchNotRetryable
	}
	rows, err := t.tx.Query(ctx, "SELECT card_id::text FROM bank.card_status_batch_items WHERE entity_id=$1 AND batch_id=$2 ORDER BY card_id", bank, sourceID)
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	defer rows.Close()
	cardIDs := make([]string, 0, source.ItemCount)
	for rows.Next() {
		var cardID string
		if err := rows.Scan(&cardID); err != nil {
			return domain.CardStatusBatch{}, nil, err
		}
		cardIDs = append(cardIDs, cardID)
	}
	if err := rows.Err(); err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	rows.Close()
	if len(cardIDs) != source.ItemCount || len(input.OperationIDs) != source.ItemCount {
		return domain.CardStatusBatch{}, nil, fmt.Errorf("batch membership does not match its item count")
	}
	batch, err := scanCardStatusBatch(t.tx.QueryRow(ctx, "INSERT INTO bank.card_status_batches(entity_id,id,target_status,reason,requested_by,requester_role,requester_entity_id,request_id,idempotency_record_id,item_count,retry_of_batch_id,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$5,$5) RETURNING id::text,target_status::text,reason,requested_by::text,requester_role,status::text,item_count,applied_count,ignored_count,failure_code,failure_summary,retry_of_batch_id::text,created_at,updated_at,completed_at", bank, input.ID, source.TargetStatus, source.Reason, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.RequestID, input.IdempotencyRecordID, source.ItemCount, sourceID))
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	items, err := t.insertCardStatusBatchItems(ctx, bank, input.ID, source.TargetStatus, source.Reason, cardIDs, input.OperationIDs, input.ActorID, input.ActorRole, input.ActorEntityID, input.RequestID, batch.CreatedAt)
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	details, err := json.Marshal(map[string]any{"retry_of_batch_id": sourceID, "item_count": source.ItemCount})
	if err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	if _, err = t.tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,'card_status_batch.retry','card_status_batch',$5,'succeeded',$6,$7)", bank, input.ActorID, input.ActorRole, nullable(input.ActorEntityID), input.ID, input.RequestID, details); err != nil {
		return domain.CardStatusBatch{}, nil, err
	}
	return batch, items, nil
}

func (t transaction) insertCardStatusBatchItems(ctx context.Context, bank, batchID, targetStatus, reason string, cardIDs, operationIDs []string, actorID, actorRole, actorEntityID, requestID string, createdAt time.Time) ([]domain.CardStatusBatchItem, error) {
	if len(cardIDs) != len(operationIDs) {
		return nil, fmt.Errorf("card and operation counts differ")
	}
	action := batchOperationAction(targetStatus)
	items := make([]domain.CardStatusBatchItem, 0, len(cardIDs))
	for index, cardID := range cardIDs {
		if _, err := t.tx.Exec(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,created_by,updated_by) VALUES ($1,$2,$3,$4,'queued',$5,$6,$7,$8,$9,$6,$6)", bank, operationIDs[index], cardID, action, reason, actorID, actorRole, nullable(actorEntityID), requestID); err != nil {
			return nil, err
		}
		var itemID string
		if err := t.tx.QueryRow(ctx, "INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES ($1,$2,$3,$4) RETURNING id::text", bank, batchID, cardID, operationIDs[index]).Scan(&itemID); err != nil {
			return nil, err
		}
		items = append(items, domain.CardStatusBatchItem{ID: itemID, CardID: cardID, OperationID: operationIDs[index], Outcome: "pending", CreatedAt: createdAt})
	}
	return items, nil
}

func batchOperationAction(targetStatus string) string {
	switch targetStatus {
	case "active":
		return "activate"
	case "suspended":
		return "suspend"
	default:
		return "close"
	}
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
