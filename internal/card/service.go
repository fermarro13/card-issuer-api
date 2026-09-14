// Package card owns card reads, issuance, lifecycle commands, and expiry retry.
package card

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"card-issuer-api/internal/auth"
	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/idempotency"
)

const (
	replayLifetime      = 7 * 24 * time.Hour
	statusOK            = 200
	statusCreated       = 201
	statusAccepted      = 202
	statusNotFound      = 404
	statusConflict      = 409
	statusUnprocessable = 422
)

type Service struct {
	reader Store
	now    func() time.Time
}

func New(reader Store) *Service { return &Service{reader: reader, now: time.Now} }

func (s *Service) ListCards(ctx context.Context, bank, clientID, accountID, productID, status string) ([]json.RawMessage, error) {
	cards, err := s.reader.Cards(ctx, bank, domain.CardFilter{ClientID: clientID, AccountID: accountID, ProductID: productID, Status: status})
	return marshalItems(cards, err)
}

func (s *Service) GetCard(ctx context.Context, bank, id string) (json.RawMessage, error) {
	value, err := s.reader.Card(ctx, bank, id)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode card: %w", err)
	}
	return raw, nil
}

func (s *Service) ListCardOperations(ctx context.Context, bank, id string) ([]json.RawMessage, error) {
	items, err := s.reader.CardOperations(ctx, bank, id)
	return marshalItems(items, err)
}

func (s *Service) ListCardStatusHistory(ctx context.Context, bank, id string) ([]json.RawMessage, error) {
	items, err := s.reader.CardStatusHistory(ctx, bank, id)
	return marshalItems(items, err)
}

func (s *Service) IssueCardWorkflow(ctx context.Context, principal auth.Principal, bank, key string, body []byte, clientID, accountID, productID, reason, requestID string) (json.RawMessage, error) {
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := "cards.issue.actor." + principal.UserID
	replay, _, found, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	valid, err := tx.IssueReferencesValid(ctx, bank, clientID, accountID, productID)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, problem(statusUnprocessable, "invalid_reference", "The card references are not active and bank-consistent.")
	}
	cardID, err := newUUID()
	if err != nil {
		return nil, err
	}
	credentialReference, err := credential()
	if err != nil {
		return nil, err
	}
	operationID, err := newUUID()
	if err != nil {
		return nil, err
	}
	card, operation, err := tx.IssueCard(ctx, bank, domain.CardIssue{CardID: cardID, OperationID: operationID, ClientID: clientID, AccountReferenceID: accountID, ProductID: productID, CredentialReference: credentialReference, Reason: reason, ActorID: principal.UserID, ActorRole: principal.Role, ActorEntityID: actorEntityID(principal), RequestID: requestID})
	if err != nil {
		return nil, err
	}
	raw, err := combined(card, operation)
	if err != nil {
		return nil, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusCreated, raw, operationID, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) CardCommandWorkflow(ctx context.Context, principal auth.Principal, bank, id, action, key string, body []byte, reason, requestID string) (json.RawMessage, int, error) {
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	scope := "cards." + action + "." + id + ".actor." + principal.UserID
	replay, status, found, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, 0, err
	}
	if found {
		return replay, status, nil
	}
	state, err := tx.LockCard(ctx, bank, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, 0, problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	if err != nil {
		return nil, 0, err
	}
	if action == "replace" {
		return s.replace(ctx, tx, principal, bank, id, scope, key, reason, requestID, state)
	}
	target, valid, ignored := transition(state.Status, action)
	if !valid {
		return nil, 0, problem(statusUnprocessable, "invalid_card_transition", "The requested card transition is not allowed.")
	}
	operationID, err := newUUID()
	if err != nil {
		return nil, 0, err
	}
	predecessorOperationID := ""
	if action == "activate" && !ignored {
		predecessorOperationID, err = newUUID()
		if err != nil {
			return nil, 0, err
		}
	}
	card, operation, err := tx.ApplyCardCommand(ctx, bank, domain.CardCommand{CardID: id, OperationID: operationID, PredecessorOperationID: predecessorOperationID, Action: action, PreviousStatus: state.Status, TargetStatus: target, Ignored: ignored, Reason: reason, ActorID: principal.UserID, ActorRole: principal.Role, ActorEntityID: actorEntityID(principal), RequestID: requestID})
	if err != nil {
		return nil, 0, err
	}
	raw, err := combined(card, operation)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusOK, raw, operationID, principal.UserID); err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return raw, statusOK, nil
}

func (s *Service) replace(ctx context.Context, tx Transaction, principal auth.Principal, bank, id, scope, key, reason, requestID string, state domain.CardState) (json.RawMessage, int, error) {
	if state.Status != "active" && state.Status != "suspended" {
		return nil, 0, problem(statusUnprocessable, "invalid_card_transition", "The card cannot be replaced in its current status.")
	}
	pending, err := tx.ReplacementPending(ctx, bank, id)
	if err != nil {
		return nil, 0, err
	}
	if pending {
		return nil, 0, problem(statusConflict, "replacement_pending", "The card already has an issued replacement.")
	}
	successor, err := newUUID()
	if err != nil {
		return nil, 0, err
	}
	credentialReference, err := credential()
	if err != nil {
		return nil, 0, err
	}
	operationID, err := newUUID()
	if err != nil {
		return nil, 0, err
	}
	card, operation, err := tx.ReplaceCard(ctx, bank, domain.CardReplacement{SuccessorID: successor, OperationID: operationID, PredecessorID: id, ClientID: state.ClientID, AccountReferenceID: state.AccountReferenceID, ProductID: state.ProductID, CredentialReference: credentialReference, Reason: reason, ActorID: principal.UserID, ActorRole: principal.Role, ActorEntityID: actorEntityID(principal), RequestID: requestID})
	if err != nil {
		return nil, 0, err
	}
	raw, err := combined(card, operation)
	if err != nil {
		return nil, 0, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusCreated, raw, operationID, principal.UserID); err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return raw, statusCreated, nil
}

func (s *Service) RetryExpiryWorkflow(ctx context.Context, principal auth.Principal, bank, runID, itemID, key string, body []byte, _ string, requestID string) error {
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope := "expiry.retry." + runID + "." + itemID + ".actor." + principal.UserID
	_, _, found, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return err
	}
	if found {
		return tx.Commit(ctx)
	}
	retryable, err := tx.RetryExpiryItem(ctx, bank, runID, itemID, principal.UserID)
	if err != nil {
		return err
	}
	if !retryable {
		return problem(statusConflict, "expiry_item_not_retryable", "The expiry item is not eligible for manual retry.")
	}
	if err = tx.RecordExpiryRetryAudit(ctx, bank, itemID, principal.UserID, requestID); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"id": itemID, "status": "pending"})
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusAccepted, raw, itemID, principal.UserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) claim(ctx context.Context, tx Transaction, bank, scope, key string, body []byte) (json.RawMessage, int, bool, error) {
	now := s.now().UTC()
	replay, err := tx.ClaimIdempotency(ctx, domain.ShardIdempotencyClaim{Bank: bank, Scope: scope, Key: key, Fingerprint: idempotency.Fingerprint(body), Now: now, ExpiresAt: now.Add(replayLifetime)})
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, 0, false, problem(statusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
	}
	return replay.Response, replay.Status, replay.Found, err
}

func transition(current, action string) (string, bool, bool) {
	target := map[string]string{"activate": "active", "suspend": "suspended", "resume": "active", "close": "closed"}[action]
	if current == target {
		return target, true, true
	}
	switch action {
	case "activate":
		return target, current == "issued", false
	case "suspend":
		return target, current == "active", false
	case "resume":
		return target, current == "suspended", false
	case "close":
		return target, current == "issued" || current == "active" || current == "suspended", false
	}
	return "", false, false
}

func actorEntityID(principal auth.Principal) string {
	if principal.Role == "bank_operator" {
		return principal.EntityID
	}
	return ""
}
func combined(card domain.Card, operation domain.CardOperation) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"card": card, "operation": operation})
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	v := hex.EncodeToString(b)
	return v[0:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
func credential() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "local_" + base64.RawURLEncoding.EncodeToString(b), nil
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
