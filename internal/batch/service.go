// Package batch owns card-status batch workflows.
package batch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"card-issuer-api/internal/auth"
	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/idempotency"
)

const (
	maxSize             = 200
	replayLifetime      = 7 * 24 * time.Hour
	statusOK            = 200
	statusCreated       = 201
	statusAccepted      = 202
	statusBadRequest    = 400
	statusNotFound      = 404
	statusConflict      = 409
	statusUnprocessable = 422
)

type Service struct {
	reader Store
	now    func() time.Time
}

func New(reader Store) *Service { return &Service{reader: reader, now: time.Now} }

type response struct {
	domain.CardStatusBatch
	Items []domain.CardStatusBatchItem `json:"items,omitempty"`
	Links map[string]string            `json:"links"`
}

func (s *Service) ListCardStatusBatches(ctx context.Context, bank string) ([]json.RawMessage, error) {
	items, err := s.reader.CardStatusBatches(ctx, bank)
	return marshalItems(items, err)
}
func (s *Service) GetCardStatusBatch(ctx context.Context, bank, id string) (json.RawMessage, error) {
	value, err := s.reader.CardStatusBatch(ctx, bank, id)
	if err != nil {
		return nil, err
	}
	return s.json(bank, value, nil)
}
func (s *Service) ListCardStatusBatchItems(ctx context.Context, bank, id string) ([]json.RawMessage, error) {
	items, err := s.reader.CardStatusBatchItems(ctx, bank, id)
	return marshalItems(items, err)
}

func (s *Service) CreateCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, key string, _ []byte, targetStatus, reason string, cardIDs []string, requestID string) (json.RawMessage, error) {
	canonicalIDs, normalized, err := normalize(targetStatus, reason, cardIDs)
	if err != nil {
		return nil, err
	}
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := "card-status-batches.create.actor." + principal.UserID
	replay, err := s.claim(ctx, tx, bank, scope, key, normalized)
	if err != nil {
		return nil, err
	}
	if replay.Found {
		return replay.Response, nil
	}
	exists, err := tx.CardsExist(ctx, bank, canonicalIDs)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, problem(statusUnprocessable, "invalid_card", "Every requested card must belong to the selected bank.")
	}
	batchID, err := newUUID()
	if err != nil {
		return nil, err
	}
	operationIDs, err := newIDs(len(canonicalIDs))
	if err != nil {
		return nil, err
	}
	batch, items, err := tx.CreateCardStatusBatchDraft(ctx, bank, domain.CardStatusBatchDraft{ID: batchID, IdempotencyRecordID: replay.RecordID, TargetStatus: strings.TrimSpace(targetStatus), Reason: strings.TrimSpace(reason), CardIDs: canonicalIDs, OperationIDs: operationIDs, ActorID: principal.UserID, ActorRole: principal.Role, ActorEntityID: actorEntityID(principal), RequestID: requestID})
	if err != nil {
		return nil, err
	}
	raw, err := s.json(bank, batch, items)
	if err != nil {
		return nil, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusCreated, raw, batch.ID, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) ExecuteCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	return s.change(ctx, principal, bank, id, key, body, requestID, "execute")
}
func (s *Service) CancelCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	return s.change(ctx, principal, bank, id, key, body, requestID, "cancel")
}

func (s *Service) RetryCardStatusBatchWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID string) (json.RawMessage, error) {
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := "card-status-batches.retry." + id + ".actor." + principal.UserID
	replay, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if replay.Found {
		return replay.Response, nil
	}
	batchID, err := newUUID()
	if err != nil {
		return nil, err
	}
	source, err := tx.LockCardStatusBatch(ctx, bank, id)
	if err != nil {
		return nil, batchProblem(err)
	}
	if source.Status != "failed" && source.Status != "cancelled" {
		return nil, problem(statusConflict, "batch_not_retryable", "Only failed or cancelled batches may be retried.")
	}
	operationIDs, err := newIDs(source.ItemCount)
	if err != nil {
		return nil, err
	}
	batch, items, err := tx.RetryCardStatusBatch(ctx, bank, id, domain.CardStatusBatchRetry{ID: batchID, IdempotencyRecordID: replay.RecordID, OperationIDs: operationIDs, ActorID: principal.UserID, ActorRole: principal.Role, ActorEntityID: actorEntityID(principal), RequestID: requestID})
	if err != nil {
		return nil, batchProblem(err)
	}
	raw, err := s.json(bank, batch, items)
	if err != nil {
		return nil, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusCreated, raw, batch.ID, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) change(ctx context.Context, principal auth.Principal, bank, id, key string, body []byte, requestID, action string) (json.RawMessage, error) {
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := "card-status-batches." + action + "." + id + ".actor." + principal.UserID
	replay, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if replay.Found {
		return replay.Response, nil
	}
	var batch domain.CardStatusBatch
	switch action {
	case "execute":
		batch, err = tx.ExecuteCardStatusBatch(ctx, bank, id, principal.UserID, principal.Role, actorEntityID(principal), requestID)
	case "cancel":
		batch, err = tx.CancelCardStatusBatch(ctx, bank, id, principal.UserID, principal.Role, actorEntityID(principal), requestID)
	default:
		return nil, fmt.Errorf("unsupported batch action %q", action)
	}
	if err != nil {
		return nil, batchProblem(err)
	}
	raw, err := s.json(bank, batch, nil)
	if err != nil {
		return nil, err
	}
	status := statusAccepted
	if action == "cancel" {
		status = statusOK
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, status, raw, batch.ID, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) claim(ctx context.Context, tx Transaction, bank, scope, key string, body []byte) (domain.ShardIdempotencyReplay, error) {
	now := s.now().UTC()
	replay, err := tx.ClaimIdempotency(ctx, domain.ShardIdempotencyClaim{Bank: bank, Scope: scope, Key: key, Fingerprint: idempotency.Fingerprint(body), Now: now, ExpiresAt: now.Add(replayLifetime)})
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return domain.ShardIdempotencyReplay{}, problem(statusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
	}
	return replay, err
}
func (s *Service) json(bank string, batch domain.CardStatusBatch, items []domain.CardStatusBatchItem) (json.RawMessage, error) {
	return json.Marshal(response{CardStatusBatch: batch, Items: items, Links: map[string]string{"execute": "/v1/banks/" + bank + "/card-status-batches/" + batch.ID + ":execute"}})
}

func normalize(targetStatus, reason string, cardIDs []string) ([]string, []byte, error) {
	targetStatus = strings.TrimSpace(targetStatus)
	reason = strings.TrimSpace(reason)
	if targetStatus != "active" && targetStatus != "suspended" && targetStatus != "closed" {
		return nil, nil, problem(statusBadRequest, "invalid_request", "target_status must be active, suspended, or closed.")
	}
	if reason == "" || len(cardIDs) == 0 || len(cardIDs) > maxSize {
		return nil, nil, problem(statusBadRequest, "invalid_request", "reason and between 1 and 200 card_ids are required.")
	}
	ids := append([]string(nil), cardIDs...)
	for _, id := range ids {
		if !isCanonicalUUID(id) {
			return nil, nil, problem(statusBadRequest, "invalid_request", "card_ids must contain canonical UUIDs.")
		}
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i-1] == ids[i] {
			return nil, nil, problem(statusBadRequest, "invalid_request", "card_ids must not contain duplicates.")
		}
	}
	raw, err := json.Marshal(struct {
		TargetStatus string   `json:"target_status"`
		Reason       string   `json:"reason"`
		CardIDs      []string `json:"card_ids"`
	}{targetStatus, reason, ids})
	return ids, raw, err
}
func newIDs(count int) ([]string, error) {
	ids := make([]string, 0, count)
	for range count {
		id, err := newUUID()
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:]), nil
}
func actorEntityID(principal auth.Principal) string {
	if principal.Role == "bank_operator" {
		return principal.EntityID
	}
	return ""
}
func batchProblem(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	if errors.Is(err, domain.ErrBatchNotCancellable) {
		return problem(statusConflict, "batch_not_cancellable", "The batch cannot be cancelled after processing begins.")
	}
	if errors.Is(err, domain.ErrBatchNotRetryable) {
		return problem(statusConflict, "batch_not_retryable", "Only failed or cancelled batches may be retried.")
	}
	return err
}
func isCanonicalUUID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value {
		return false
	}
	for i, char := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
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
func marshalItems[T any](items []T, err error) ([]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	raws := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		raws = append(raws, raw)
	}
	return raws, nil
}

type apiError struct {
	status       int
	code, detail string
}

func (e *apiError) Error() string                      { return e.detail }
func (e *apiError) HTTPProblem() (int, string, string) { return e.status, e.code, e.detail }
func problem(status int, code, detail string) error {
	return &apiError{status: status, code: code, detail: detail}
}
