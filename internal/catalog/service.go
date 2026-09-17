// Package catalog owns bank card products, clients, and account references.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"card-issuer-api/internal/auth"
	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/idempotency"
)

const (
	replayLifetime = 7 * 24 * time.Hour
	statusOK       = 200
	statusCreated  = 201
	statusNotFound = 404
	statusConflict = 409
)

type Service struct {
	reader        Store
	fingerprinter *idempotency.Fingerprinter
	now           func() time.Time
}

func New(reader Store, fingerprinter *idempotency.Fingerprinter) *Service {
	return &Service{reader: reader, fingerprinter: fingerprinter, now: time.Now}
}

func (s *Service) ListReference(ctx context.Context, bank, kind string) ([]json.RawMessage, error) {
	switch kind {
	case "card-products":
		items, err := s.reader.CardProducts(ctx, bank)
		return marshalItems(items, err)
	case "clients":
		items, err := s.reader.Clients(ctx, bank)
		return marshalItems(items, err)
	case "account-references":
		items, err := s.reader.AccountReferences(ctx, bank)
		return marshalItems(items, err)
	default:
		return nil, fmt.Errorf("unsupported reference kind %q", kind)
	}
}

func (s *Service) GetReference(ctx context.Context, bank, kind, id string) (json.RawMessage, error) {
	var (
		value any
		err   error
	)
	switch kind {
	case "card-products":
		value, err = s.reader.CardProduct(ctx, bank, id)
	case "clients":
		value, err = s.reader.Client(ctx, bank, id)
	case "account-references":
		value, err = s.reader.AccountReference(ctx, bank, id)
	default:
		return nil, fmt.Errorf("unsupported reference kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func (s *Service) CreateCardProductWorkflow(ctx context.Context, principal auth.Principal, bank, key string, _ []byte, productCode, name, status string, configuration json.RawMessage, requestID string) (json.RawMessage, error) {
	input := struct {
		ProductCode   string          `json:"product_code"`
		Name          string          `json:"name"`
		Status        string          `json:"status"`
		Configuration json.RawMessage `json:"configuration"`
	}{ProductCode: productCode, Name: name, Status: status, Configuration: configuration}
	return s.mutate(ctx, principal, bank, "card-products", key, input, requestID, func(tx Transaction) (json.RawMessage, string, error) {
		product, err := tx.CreateCardProduct(ctx, bank, domain.CardProductCreate{ProductCode: productCode, Name: name, Status: status, Configuration: configuration, ActorID: principal.UserID})
		if err != nil {
			return nil, "", err
		}
		raw, err := json.Marshal(product)
		return raw, product.ID, err
	})
}

func (s *Service) CreateClientWorkflow(ctx context.Context, principal auth.Principal, bank, key string, _ []byte, external string, display *string, requestID string) (json.RawMessage, error) {
	input := struct {
		External string  `json:"external_client_ref"`
		Display  *string `json:"display_name"`
	}{External: external, Display: display}
	return s.mutate(ctx, principal, bank, "clients", key, input, requestID, func(tx Transaction) (json.RawMessage, string, error) {
		client, err := tx.CreateClient(ctx, bank, domain.ClientCreate{ExternalClientRef: external, DisplayName: display, ActorID: principal.UserID})
		if err != nil {
			return nil, "", err
		}
		raw, err := json.Marshal(client)
		return raw, client.ID, err
	})
}

func (s *Service) CreateAccountReferenceWorkflow(ctx context.Context, principal auth.Principal, bank, key string, _ []byte, clientID, external, requestID string) (json.RawMessage, error) {
	input := struct {
		ClientID string `json:"client_id"`
		External string `json:"external_account_ref"`
	}{ClientID: clientID, External: external}
	return s.mutate(ctx, principal, bank, "account-references", key, input, requestID, func(tx Transaction) (json.RawMessage, string, error) {
		account, err := tx.CreateAccountReference(ctx, bank, domain.AccountReferenceCreate{ClientID: clientID, ExternalAccountRef: external, ActorID: principal.UserID})
		if err != nil {
			return nil, "", err
		}
		raw, err := json.Marshal(account)
		return raw, account.ID, err
	})
}

func (s *Service) PatchCardProductWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, _ []byte, name, status *string, configuration json.RawMessage, requestID string) (json.RawMessage, error) {
	input := struct {
		Name          *string         `json:"name"`
		Status        *string         `json:"status"`
		Configuration json.RawMessage `json:"configuration"`
	}{Name: name, Status: status, Configuration: configuration}
	return s.patch(ctx, principal, bank, "card-products", id, key, input, requestID, func(tx Transaction) (json.RawMessage, bool, error) {
		product, found, err := tx.UpdateCardProduct(ctx, bank, id, domain.CardProductPatch{Name: name, Status: status, Configuration: configuration, ActorID: principal.UserID})
		if err != nil || !found {
			return nil, found, err
		}
		raw, err := json.Marshal(product)
		return raw, true, err
	})
}

func (s *Service) PatchClientWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, _ []byte, display *string, requestID string) (json.RawMessage, error) {
	input := struct {
		Display *string `json:"display_name"`
	}{Display: display}
	return s.patch(ctx, principal, bank, "clients", id, key, input, requestID, func(tx Transaction) (json.RawMessage, bool, error) {
		client, found, err := tx.UpdateClient(ctx, bank, id, domain.ClientPatch{DisplayName: display, ActorID: principal.UserID})
		if err != nil || !found {
			return nil, found, err
		}
		raw, err := json.Marshal(client)
		return raw, true, err
	})
}

func (s *Service) PatchAccountReferenceWorkflow(ctx context.Context, principal auth.Principal, bank, id, key string, _ []byte, clientID, requestID string) (json.RawMessage, error) {
	input := struct {
		ClientID string `json:"client_id"`
	}{ClientID: clientID}
	return s.patch(ctx, principal, bank, "account-references", id, key, input, requestID, func(tx Transaction) (json.RawMessage, bool, error) {
		account, found, err := tx.UpdateAccountReference(ctx, bank, id, domain.AccountReferencePatch{ClientID: clientID, ActorID: principal.UserID})
		if err != nil || !found {
			return nil, found, err
		}
		raw, err := json.Marshal(account)
		return raw, true, err
	})
}

type createFunc func(Transaction) (json.RawMessage, string, error)
type patchFunc func(Transaction) (json.RawMessage, bool, error)

func referenceAudit(principal auth.Principal, bank, action, kind, resourceID, requestID string) domain.ReferenceAudit {
	actorEntityID := ""
	if principal.Role == "bank_operator" {
		actorEntityID = principal.EntityID
	}
	return domain.ReferenceAudit{
		Bank:          bank,
		ActorID:       principal.UserID,
		ActorRole:     principal.Role,
		ActorEntityID: actorEntityID,
		Action:        action,
		Kind:          kind,
		ResourceID:    resourceID,
		RequestID:     requestID,
	}
}

func (s *Service) mutate(ctx context.Context, principal auth.Principal, bank, kind, key string, input any, requestID string, create createFunc) (json.RawMessage, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := kind + ".create.actor." + principal.UserID
	replay, _, found, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	raw, id, err := create(tx)
	if err != nil {
		return nil, err
	}
	if err = tx.RecordReferenceAudit(ctx, referenceAudit(principal, bank, kind+".create", kind, id, requestID)); err != nil {
		return nil, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusCreated, raw, id, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) patch(ctx context.Context, principal auth.Principal, bank, kind, id, key string, input any, requestID string, patch patchFunc) (json.RawMessage, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.reader.Transaction(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := kind + ".patch." + id + ".actor." + principal.UserID
	replay, _, found, err := s.claim(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	raw, updated, err := patch(tx)
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	if err = tx.RecordReferenceAudit(ctx, referenceAudit(principal, bank, kind+".patch", kind, id, requestID)); err != nil {
		return nil, err
	}
	if err = tx.FinishIdempotency(ctx, bank, scope, key, statusOK, raw, id, principal.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) claim(ctx context.Context, tx Transaction, bank, scope, key string, body []byte) (json.RawMessage, int, bool, error) {
	now := s.now().UTC()
	replay, err := tx.ClaimIdempotency(ctx, domain.ShardIdempotencyClaim{Bank: bank, Scope: scope, Key: key, Fingerprint: s.fingerprinter.Fingerprint(body), Now: now, ExpiresAt: now.Add(replayLifetime)})
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, 0, false, problem(statusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
	}
	return replay.Response, replay.Status, replay.Found, err
}

func marshalItems[T any](items []T, err error) ([]json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	raw := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		value, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		raw = append(raw, value)
	}
	return raw, nil
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
