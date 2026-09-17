// Package bank owns bank-directory reads, provisioning, and updates.
package bank

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"card-issuer-api/internal/auth"
	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/idempotency"
	"card-issuer-api/internal/tenant"
)

const (
	replayLifetime = 7 * 24 * time.Hour
	statusOK       = 200
	statusCreated  = 201
	statusAccepted = 202
	statusNotFound = 404
	statusConflict = 409
)

type Service struct {
	control       ControlStore
	routes        RouteStore
	reader        EntityStore
	shardID       string
	tenant        *tenant.Service
	fingerprinter *idempotency.Fingerprinter
	now           func() time.Time
}

func New(control ControlStore, routes RouteStore, reader EntityStore, shardID string, fingerprinter *idempotency.Fingerprinter) *Service {
	return &Service{control: control, routes: routes, reader: reader, shardID: shardID, tenant: tenant.New(routes, shardID), fingerprinter: fingerprinter, now: time.Now}
}

func (s *Service) ListBanks(ctx context.Context) ([]json.RawMessage, error) {
	routes, err := s.routes.Banks(ctx)
	if err != nil {
		return nil, err
	}
	data := make([]json.RawMessage, 0)
	for _, route := range routes {
		if route.ShardID != s.shardID {
			continue
		}
		entity, err := s.entity(ctx, route.EntityID)
		if err != nil {
			continue
		}
		var value map[string]any
		_ = json.Unmarshal(entity, &value)
		value["routing_status"] = route.PlacementStatus
		raw, _ := json.Marshal(value)
		data = append(data, raw)
	}
	return data, nil
}

func (s *Service) GetBank(ctx context.Context, id string) (json.RawMessage, error) {
	if !s.tenant.ActiveOrProvisioning(ctx, id) {
		return nil, domain.ErrNotFound
	}
	return s.entity(ctx, id)
}

func (s *Service) ProvisionBankWorkflow(ctx context.Context, principal auth.Principal, key string, body []byte, reference, name, requestID string) (int, json.RawMessage, error) {
	tx, err := s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "banks.create.actor." + principal.UserID
	replay, found, err := s.centralIdempotency(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	var id string
	if found {
		var saved map[string]any
		_ = json.Unmarshal(replay, &saved)
		id, _ = saved["id"].(string)
		if id == "" {
			return statusAccepted, replay, nil
		}
		if _, provisioning := saved["provisioning"]; !provisioning {
			return statusCreated, replay, nil
		}
	} else {
		id, err = newUUID()
		if err != nil {
			return 0, nil, err
		}
		if err = tx.CreateBankRoute(ctx, id, s.shardID); err != nil {
			return 0, nil, err
		}
		provisional, _ := json.Marshal(map[string]any{"id": id, "provisioning": true})
		if err = tx.FinishIdempotency(ctx, scope, key, statusAccepted, provisional, id); err != nil {
			return 0, nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}

	// Preserve the existing saga order: central provisioning route, shard entity,
	// then central active route.
	shardTx, err := s.reader.Transaction(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	if err = shardTx.ProvisionEntity(ctx, domain.EntityProvision{ID: id, BankReference: reference, Name: name, ActorID: principal.UserID, RequestID: requestID}); err != nil {
		shardTx.Rollback(ctx)
		return 0, nil, err
	}
	if err = shardTx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	raw, err := s.entity(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	tx, err = s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	if err = tx.SetBankRouteStatus(ctx, id, s.shardID, "active"); err != nil {
		return 0, nil, err
	}
	if err = tx.FinishIdempotency(ctx, scope, key, statusCreated, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return statusCreated, raw, nil
}

func (s *Service) PatchBankWorkflow(ctx context.Context, principal auth.Principal, id, key string, body []byte, name, status *string, requestID string) (int, json.RawMessage, error) {
	tx, err := s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "banks.patch." + id + ".actor." + principal.UserID
	replay, found, err := s.centralIdempotency(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found && string(replay) != "{}" {
		tx.Rollback(ctx)
		return statusOK, replay, nil
	}
	if !s.tenant.ActiveOrProvisioning(ctx, id) {
		return 0, nil, problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	if status != nil {
		placement := "paused"
		if *status == "active" {
			placement = "active"
		}
		if err = tx.SetBankRouteStatusForEntity(ctx, id, placement); err != nil {
			return 0, nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	shardTx, err := s.reader.Transaction(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	defer shardTx.Rollback(ctx)
	entity, err := shardTx.UpdateEntity(ctx, domain.EntityPatch{ID: id, Name: name, Status: status, ActorID: principal.UserID, RequestID: requestID})
	if err != nil {
		return 0, nil, err
	}
	raw, err := json.Marshal(entity)
	if err != nil {
		return 0, nil, err
	}
	if err = shardTx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	tx, err = s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	if err = tx.FinishIdempotency(ctx, scope, key, statusOK, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return statusOK, raw, nil
}

func (s *Service) entity(ctx context.Context, id string) (json.RawMessage, error) {
	entity, err := s.reader.Entity(ctx, id)
	if err != nil {
		return nil, err
	}
	// PostgreSQL jsonb replays object keys in length-then-lexical order. Keep
	// the first HTTP response in that same stable order.
	raw, err := json.Marshal(struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Status        string    `json:"status"`
		CreatedAt     time.Time `json:"created_at"`
		UpdatedAt     time.Time `json:"updated_at"`
		BankReference string    `json:"bank_reference"`
	}{entity.ID, entity.Name, entity.Status, entity.CreatedAt, entity.UpdatedAt, entity.BankReference})
	return raw, err
}

func (s *Service) centralIdempotency(ctx context.Context, tx ControlTransaction, scope, key string, body []byte) (json.RawMessage, bool, error) {
	now := s.now().UTC()
	replay, err := tx.ClaimIdempotency(ctx, domain.CentralIdempotencyClaim{Scope: scope, Key: key, Fingerprint: s.fingerprinter.Fingerprint(body), Now: now, ExpiresAt: now.Add(replayLifetime)})
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, false, problem(statusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
	}
	return replay.Response, replay.Found, err
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

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:]), nil
}
