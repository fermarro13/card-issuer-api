// Package staff owns issuer staff-directory administration.
package staff

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	statusBadReq   = 400
	statusNotFound = 404
	statusConflict = 409
)

type Service struct {
	control       DirectoryStore
	tenant        *tenant.Service
	fingerprinter *idempotency.Fingerprinter
	now           func() time.Time
}

func New(control DirectoryStore, routes RouteStore, shardID string, fingerprinter *idempotency.Fingerprinter) *Service {
	return &Service{control: control, tenant: tenant.New(routes, shardID), fingerprinter: fingerprinter, now: time.Now}
}

func (s *Service) ListUsers(ctx context.Context) ([]json.RawMessage, error) {
	users, err := s.control.Users(ctx)
	if err != nil {
		return nil, err
	}
	data := make([]json.RawMessage, 0, len(users))
	for _, user := range users {
		data = append(data, userJSON(user))
	}
	return data, nil
}

func (s *Service) GetUser(ctx context.Context, id string) (json.RawMessage, error) {
	user, err := s.control.User(ctx, id)
	if err != nil {
		return nil, err
	}
	return userJSON(user), nil
}

func (s *Service) CreateUserWorkflow(ctx context.Context, principal auth.Principal, key string, body []byte, username, password, role, entityID, requestID string) (int, json.RawMessage, error) {
	tx, err := s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.create.actor." + principal.UserID
	replay, found, err := s.centralIdempotency(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return statusCreated, replay, nil
	}
	if entityID != "" && !s.tenant.Active(ctx, entityID) {
		return 0, nil, problem(statusBadReq, "invalid_request", "The bank assignment must be active.")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, nil, err
	}
	user, err := tx.CreateUser(ctx, domain.StaffUserCreate{Username: strings.ToLower(strings.TrimSpace(username)), PasswordHash: hash, Role: role, EntityID: entityID, ActorID: principal.UserID})
	if err != nil {
		return 0, nil, err
	}
	if err = tx.RecordAuthenticationAudit(ctx, domain.AuthenticationAudit{EventType: "user_provision", ActorUserID: principal.UserID, SubjectUserID: user.ID, EntityID: entityID, RequestID: requestID}); err != nil {
		return 0, nil, err
	}
	raw := userJSON(user)
	if err = tx.FinishIdempotency(ctx, scope, key, statusCreated, raw, user.ID); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return statusCreated, raw, nil
}

func (s *Service) UpdateUserWorkflow(ctx context.Context, principal auth.Principal, id, key string, body []byte, role, entityID, status, requestID string) (int, json.RawMessage, error) {
	tx, err := s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.update." + id + ".actor." + principal.UserID
	if status != "" {
		scope = "users.disable." + id + ".actor." + principal.UserID
	}
	replay, found, err := s.centralIdempotency(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return statusOK, replay, nil
	}
	if entityID != "" && !s.tenant.Active(ctx, entityID) {
		return 0, nil, problem(statusBadReq, "invalid_request", "The bank assignment must be active.")
	}
	var user domain.DirectoryUser
	var updated bool
	if role != "" {
		user, updated, err = tx.UpdateUserRole(ctx, id, role, entityID, principal.UserID)
	} else {
		user, updated, err = tx.UpdateUserStatus(ctx, id, status, principal.UserID)
	}
	if err != nil {
		return 0, nil, err
	}
	if !updated {
		return 0, nil, problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	raw := userJSON(user)
	if err = tx.RecordAuthenticationAudit(ctx, domain.AuthenticationAudit{EventType: scope, ActorUserID: principal.UserID, SubjectUserID: id, RequestID: requestID}); err != nil {
		return 0, nil, err
	}
	if err = tx.FinishIdempotency(ctx, scope, key, statusOK, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return statusOK, raw, nil
}

func (s *Service) SetUserPasswordWorkflow(ctx context.Context, principal auth.Principal, id, key string, body []byte, password, requestID string) (int, json.RawMessage, error) {
	tx, err := s.control.Transaction(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.set_password." + id + ".actor." + principal.UserID
	replay, found, err := s.centralIdempotency(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return statusOK, replay, nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, nil, err
	}
	user, updated, err := tx.UpdateUserPassword(ctx, id, hash, principal.UserID)
	if err != nil {
		return 0, nil, err
	}
	if !updated {
		return 0, nil, problem(statusNotFound, "not_found", "The requested resource does not exist.")
	}
	raw := userJSON(user)
	if err = tx.RecordAuthenticationAudit(ctx, domain.AuthenticationAudit{EventType: "user_password_reset", ActorUserID: principal.UserID, SubjectUserID: id, RequestID: requestID}); err != nil {
		return 0, nil, err
	}
	if err = tx.FinishIdempotency(ctx, scope, key, statusOK, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return statusOK, raw, nil
}

func (s *Service) centralIdempotency(ctx context.Context, tx Transaction, scope, key string, body []byte) (json.RawMessage, bool, error) {
	now := s.now().UTC()
	replay, err := tx.ClaimIdempotency(ctx, domain.CentralIdempotencyClaim{Scope: scope, Key: key, Fingerprint: s.fingerprinter.Fingerprint(body), Now: now, ExpiresAt: now.Add(replayLifetime)})
	if errors.Is(err, domain.ErrIdempotencyConflict) {
		return nil, false, problem(statusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
	}
	return replay.Response, replay.Found, err
}

func userJSON(user domain.DirectoryUser) json.RawMessage {
	var entity any
	if user.EntityID != "" {
		entity = user.EntityID
	}
	raw, _ := json.Marshal(map[string]any{"id": user.ID, "username": user.Username, "role": user.Role, "entity_id": entity, "status": user.Status, "created_at": user.CreatedAt.UTC(), "updated_at": user.UpdatedAt.UTC()})
	return raw
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
