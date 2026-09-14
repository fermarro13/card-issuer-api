// Package resource implements the non-authentication public API. It deliberately
// keeps routing, authorization, and tenant-scoped SQL in one small boundary.
package resource

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"card-issuer-api/internal/auth"
	"card-issuer-api/internal/server"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const replayLifetime = 7 * 24 * time.Hour

type API struct {
	auth    auth.API
	admin   *pgxpool.Pool // ci_auth_runtime: staff and controlled directory writes.
	routing *pgxpool.Pool // ci_routing_reader: route lookups only.
	shard   *pgxpool.Pool
	shardID string
	key     []byte
	now     func() time.Time
}

func New(authentication auth.API, admin, routing, shard *pgxpool.Pool, shardID string, cursorKey []byte) *API {
	return &API{auth: authentication, admin: admin, routing: routing, shard: shard, shardID: shardID, key: append([]byte(nil), cursorKey...), now: time.Now}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	if path == "banks" {
		a.banks(w, r, claims)
		return
	}
	if path == "users" {
		a.users(w, r, claims)
		return
	}
	if strings.HasPrefix(path, "users/") {
		a.user(w, r, claims, strings.TrimPrefix(path, "users/"))
		return
	}
	if strings.HasPrefix(path, "banks/") {
		a.bankResource(w, r, claims, strings.TrimPrefix(path, "banks/"))
		return
	}
	server.WriteProblem(w, r, http.StatusNotFound, "not_found", "The requested API endpoint does not exist.")
}

func (a *API) authenticate(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	p := strings.Fields(r.Header.Get("Authorization"))
	if len(p) != 2 || !strings.EqualFold(p[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		server.WriteProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	c, err := a.auth.ValidateAccess(p[1])
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		server.WriteProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	return c, true
}

func issuerOperator(c auth.Claims) bool { return c.Role == "issuer_operator" }
func issuer(c auth.Claims) bool         { return c.Role == "issuer_operator" || c.Role == "issuer_readonly" }
func canRead(c auth.Claims) bool {
	return issuer(c) || c.Role == "bank_operator" || c.Role == "bank_readonly"
}
func canLifecycle(c auth.Claims) bool { return issuerOperator(c) || c.Role == "bank_operator" }

func (a *API) banks(w http.ResponseWriter, r *http.Request, c auth.Claims) {
	if !issuerOperator(c) {
		a.forbidden(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// Directory routes are the stable list source. Entity details are fetched with RLS below.
		rows, err := a.routing.Query(r.Context(), "SELECT entity_id::text,shard_id,placement_status,created_at FROM control.bank_routing_entries ORDER BY created_at DESC,entity_id DESC")
		if err != nil {
			a.internal(w, r)
			return
		}
		defer rows.Close()
		data := make([]json.RawMessage, 0)
		for rows.Next() {
			var id, sid, status string
			var created time.Time
			if err := rows.Scan(&id, &sid, &status, &created); err != nil {
				a.internal(w, r)
				return
			}
			if sid != a.shardID {
				continue
			}
			entity, err := a.entity(r.Context(), id)
			if err != nil {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(entity, &m)
			m["routing_status"] = status
			raw, _ := json.Marshal(m)
			data = append(data, raw)
		}
		if rows.Err() != nil {
			a.internal(w, r)
			return
		}
		a.collection(w, r, "banks", "directory", data)
	case http.MethodPost:
		var in struct {
			BankReference string `json:"bank_reference"`
			Name          string `json:"name"`
		}
		if !decode(w, r, &in) || strings.TrimSpace(in.BankReference) == "" || strings.TrimSpace(in.Name) == "" {
			a.invalid(w, r, "bank_reference and name are required.")
			return
		}
		key, ok := idempotency(w, r)
		if !ok {
			return
		}
		body, _ := normalized(in)
		status, result, err := a.provisionBank(r.Context(), c, key, body, in.BankReference, in.Name, server.RequestID(r))
		if err != nil {
			a.handle(w, r, err)
			return
		}
		server.WriteJSON(w, status, result)
	default:
		method(w, r, http.MethodGet, http.MethodPost)
	}
}

func (a *API) bankResource(w http.ResponseWriter, r *http.Request, c auth.Claims, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) < 1 || !uuid(parts[0]) {
		a.notFound(w, r)
		return
	}
	bank := parts[0]
	if len(parts) == 1 {
		a.bank(w, r, c, bank)
		return
	}
	if !canRead(c) || !a.allowedBank(c, bank) {
		a.forbidden(w, r)
		return
	}
	switch parts[1] {
	case "card-products":
		a.reference(w, r, c, bank, "card-products", parts[2:])
	case "clients":
		a.reference(w, r, c, bank, "clients", parts[2:])
	case "account-references":
		a.reference(w, r, c, bank, "account-references", parts[2:])
	case "cards":
		a.cards(w, r, c, bank, parts[2:])
	case "card-expiry-runs":
		a.expiry(w, r, c, bank, parts[2:])
	default:
		a.notFound(w, r)
	}
}

func (a *API) bank(w http.ResponseWriter, r *http.Request, c auth.Claims, id string) {
	if !issuerOperator(c) {
		a.forbidden(w, r)
		return
	}
	if r.Method == http.MethodGet {
		if !a.activeOrProvisioning(r.Context(), id) {
			a.notFound(w, r)
			return
		}
		raw, err := a.entity(r.Context(), id)
		if err != nil {
			a.notFound(w, r)
			return
		}
		server.WriteJSON(w, http.StatusOK, raw)
		return
	}
	if r.Method != http.MethodPatch {
		method(w, r, http.MethodGet, http.MethodPatch)
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Status *string `json:"status"`
	}
	if !decode(w, r, &in) || (in.Name == nil && in.Status == nil) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Status != nil && *in.Status != "active" && *in.Status != "inactive") {
		a.invalid(w, r, "A valid name or status is required.")
		return
	}
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	body, _ := normalized(in)
	status, result, err := a.patchBank(r.Context(), c, id, key, body, in, server.RequestID(r))
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, status, result)
}

func (a *API) users(w http.ResponseWriter, r *http.Request, c auth.Claims) {
	if !issuerOperator(c) {
		a.forbidden(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := a.admin.Query(r.Context(), "SELECT id::text,normalized_username,role,COALESCE(entity_id::text,''),status,created_at,updated_at FROM control.users ORDER BY created_at DESC,id DESC")
		if err != nil {
			a.internal(w, r)
			return
		}
		defer rows.Close()
		data := make([]json.RawMessage, 0)
		for rows.Next() {
			var id, u, role, entity, status string
			var created, updated time.Time
			if err := rows.Scan(&id, &u, &role, &entity, &status, &created, &updated); err != nil {
				a.internal(w, r)
				return
			}
			data = append(data, userJSON(id, u, role, entity, status, created, updated))
		}
		if rows.Err() != nil {
			a.internal(w, r)
			return
		}
		a.collection(w, r, "users", "control", data)
	case http.MethodPost:
		var in userInput
		if !decode(w, r, &in) || !validUser(in) || auth.ValidateNewPassword(in.Password) != nil {
			a.invalid(w, r, "username, password, role, and a valid bank assignment are required.")
			return
		}
		key, ok := idempotency(w, r)
		if !ok {
			return
		}
		body, _ := normalized(in)
		status, result, err := a.createUser(r.Context(), c, key, body, in, server.RequestID(r))
		if err != nil {
			a.handle(w, r, err)
			return
		}
		server.WriteJSON(w, status, result)
	default:
		method(w, r, http.MethodGet, http.MethodPost)
	}
}

type userInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
	EntityID string `json:"entity_id"`
}

func validRole(role string) bool {
	return role == "issuer_operator" || role == "issuer_readonly" || role == "bank_operator" || role == "bank_readonly"
}
func validUser(in userInput) bool {
	return strings.TrimSpace(in.Username) != "" && validRole(in.Role) && ((in.Role == "bank_operator" || in.Role == "bank_readonly") == uuid(in.EntityID))
}
func userJSON(id, u, role, entity, status string, created, updated time.Time) json.RawMessage {
	var e any
	if entity != "" {
		e = entity
	}
	b, _ := json.Marshal(map[string]any{"id": id, "username": u, "role": role, "entity_id": e, "status": status, "created_at": created.UTC(), "updated_at": updated.UTC()})
	return b
}

func (a *API) user(w http.ResponseWriter, r *http.Request, c auth.Claims, tail string) {
	if !issuerOperator(c) {
		a.forbidden(w, r)
		return
	}
	pieces := strings.Split(tail, ":")
	if !uuid(pieces[0]) {
		a.notFound(w, r)
		return
	}
	id := pieces[0]
	if len(pieces) == 1 && r.Method == http.MethodGet {
		raw, err := a.loadUser(r.Context(), id)
		if err != nil {
			a.notFound(w, r)
			return
		}
		server.WriteJSON(w, http.StatusOK, raw)
		return
	}
	if len(pieces) == 1 && r.Method == http.MethodPatch {
		var in struct {
			Role     *string `json:"role"`
			EntityID *string `json:"entity_id"`
		}
		if !decode(w, r, &in) || in.Role == nil || !validRole(*in.Role) || ((*in.Role == "bank_operator" || *in.Role == "bank_readonly") != (in.EntityID != nil && uuid(*in.EntityID))) {
			a.invalid(w, r, "role and its required bank assignment are invalid.")
			return
		}
		key, ok := idempotency(w, r)
		if !ok {
			return
		}
		body, _ := normalized(in)
		status, result, err := a.updateUser(r.Context(), c, id, key, body, *in.Role, value(in.EntityID), "", server.RequestID(r))
		if err != nil {
			a.handle(w, r, err)
			return
		}
		server.WriteJSON(w, status, result)
		return
	}
	if len(pieces) == 2 && pieces[1] == "disable" && r.Method == http.MethodPost {
		key, ok := idempotency(w, r)
		if !ok {
			return
		}
		status, result, err := a.updateUser(r.Context(), c, id, key, []byte("{}"), "", "", "disabled", server.RequestID(r))
		if err != nil {
			a.handle(w, r, err)
			return
		}
		server.WriteJSON(w, status, result)
		return
	}
	if len(pieces) == 2 && pieces[1] == "set-password" && r.Method == http.MethodPost {
		var in struct {
			Password string `json:"new_password"`
		}
		if !decode(w, r, &in) || auth.ValidateNewPassword(in.Password) != nil {
			a.invalid(w, r, "new_password must meet the password policy.")
			return
		}
		key, ok := idempotency(w, r)
		if !ok {
			return
		}
		body, _ := normalized(in)
		status, result, err := a.setPassword(r.Context(), c, id, key, body, in.Password, server.RequestID(r))
		if err != nil {
			a.handle(w, r, err)
			return
		}
		server.WriteJSON(w, status, result)
		return
	}
	method(w, r, http.MethodGet, http.MethodPatch, http.MethodPost)
}

func (a *API) reference(w http.ResponseWriter, r *http.Request, c auth.Claims, bank, kind string, tail []string) {
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			a.listReference(w, r, bank, kind)
			return
		}
		if r.Method == http.MethodPost && issuerOperator(c) {
			a.createReference(w, r, c, bank, kind)
			return
		}
		if r.Method == http.MethodPost {
			a.forbidden(w, r)
			return
		}
		method(w, r, http.MethodGet, http.MethodPost)
		return
	}
	if len(tail) == 1 && uuid(tail[0]) {
		if r.Method == http.MethodGet {
			a.getReference(w, r, bank, kind, tail[0])
			return
		}
		if r.Method == http.MethodPatch && issuerOperator(c) {
			a.patchReference(w, r, c, bank, kind, tail[0])
			return
		}
		if r.Method == http.MethodPatch {
			a.forbidden(w, r)
			return
		}
	}
	a.notFound(w, r)
}

func refSQL(kind string) (table, fields string) {
	switch kind {
	case "card-products":
		return "bank.card_products", "id::text,product_code,name,status,configuration,configuration_version,created_at,updated_at"
	case "clients":
		return "bank.clients", "id::text,external_client_ref,display_name,created_at,updated_at"
	default:
		return "bank.account_references", "id::text,client_id::text,external_account_ref,created_at,updated_at"
	}
}
func (a *API) listReference(w http.ResponseWriter, r *http.Request, bank, kind string) {
	table, fields := refSQL(kind)
	raws, err := a.list(r.Context(), bank, kind, "SELECT "+fields+" FROM "+table+" WHERE entity_id=$1 ORDER BY created_at DESC,id DESC", nil)
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.collection(w, r, kind, bank, raws)
}
func (a *API) getReference(w http.ResponseWriter, r *http.Request, bank, kind, id string) {
	table, fields := refSQL(kind)
	raw, err := a.one(r.Context(), bank, "SELECT "+fields+" FROM "+table+" WHERE entity_id=$1 AND id=$2", id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	server.WriteJSON(w, http.StatusOK, raw)
}

func (a *API) createReference(w http.ResponseWriter, r *http.Request, c auth.Claims, bank, kind string) {
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	var err error
	switch kind {
	case "card-products":
		var in struct {
			ProductCode   string          `json:"product_code"`
			Name          string          `json:"name"`
			Status        string          `json:"status"`
			Configuration json.RawMessage `json:"configuration"`
		}
		if !decode(w, r, &in) || strings.TrimSpace(in.ProductCode) == "" || strings.TrimSpace(in.Name) == "" || (in.Status != "" && in.Status != "active" && in.Status != "inactive") || !object(in.Configuration) {
			a.invalid(w, r, "product_code, name, optional status, and object configuration are required.")
			return
		}
		if in.Status == "" {
			in.Status = "active"
		}
		if in.Configuration == nil {
			in.Configuration = json.RawMessage(`{}`)
		}
		raw, err = a.mutateRef(r.Context(), c, bank, kind, key, in, `INSERT INTO bank.card_products(entity_id,product_code,name,status,configuration,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$6,$6) RETURNING id::text`, []any{in.ProductCode, in.Name, in.Status, in.Configuration}, server.RequestID(r))
	case "clients":
		var in struct {
			External string  `json:"external_client_ref"`
			Display  *string `json:"display_name"`
		}
		if !decode(w, r, &in) || strings.TrimSpace(in.External) == "" {
			a.invalid(w, r, "external_client_ref is required.")
			return
		}
		raw, err = a.mutateRef(r.Context(), c, bank, kind, key, in, `INSERT INTO bank.clients(entity_id,external_client_ref,display_name,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) RETURNING id::text`, []any{in.External, in.Display}, server.RequestID(r))
	default:
		var in struct {
			ClientID string `json:"client_id"`
			External string `json:"external_account_ref"`
		}
		if !decode(w, r, &in) || !uuid(in.ClientID) || strings.TrimSpace(in.External) == "" {
			a.invalid(w, r, "client_id and external_account_ref are required.")
			return
		}
		raw, err = a.mutateRef(r.Context(), c, bank, kind, key, in, `INSERT INTO bank.account_references(entity_id,client_id,external_account_ref,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) RETURNING id::text`, []any{in.ClientID, in.External}, server.RequestID(r))
	}
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusCreated, raw)
}

func (a *API) patchReference(w http.ResponseWriter, r *http.Request, c auth.Claims, bank, kind, id string) {
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	var err error
	switch kind {
	case "card-products":
		var in struct {
			Name          *string         `json:"name"`
			Status        *string         `json:"status"`
			Configuration json.RawMessage `json:"configuration"`
		}
		if !decode(w, r, &in) || (in.Name == nil && in.Status == nil && in.Configuration == nil) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Status != nil && *in.Status != "active" && *in.Status != "inactive") || !objectOrNil(in.Configuration) {
			a.invalid(w, r, "A valid name, status, or object configuration is required.")
			return
		}
		raw, err = a.patchRef(r.Context(), c, bank, kind, id, key, in, `UPDATE bank.card_products SET name=COALESCE($2,name),status=COALESCE($3,status),configuration=COALESCE($4,configuration),configuration_version=configuration_version+CASE WHEN $4::jsonb IS NULL OR configuration=$4::jsonb THEN 0 ELSE 1 END,updated_by=$5 WHERE entity_id=$1 AND id=$6`, []any{in.Name, in.Status, in.Configuration, c.UserID, id}, server.RequestID(r))
	case "clients":
		var in struct {
			Display *string `json:"display_name"`
		}
		if !decode(w, r, &in) || in.Display == nil {
			a.invalid(w, r, "display_name is required.")
			return
		}
		raw, err = a.patchRef(r.Context(), c, bank, kind, id, key, in, `UPDATE bank.clients SET display_name=$2,updated_by=$3 WHERE entity_id=$1 AND id=$4`, []any{in.Display, c.UserID, id}, server.RequestID(r))
	default:
		var in struct {
			ClientID string `json:"client_id"`
		}
		if !decode(w, r, &in) || !uuid(in.ClientID) {
			a.invalid(w, r, "client_id is required.")
			return
		}
		raw, err = a.patchRef(r.Context(), c, bank, kind, id, key, in, `UPDATE bank.account_references SET client_id=$2,updated_by=$3 WHERE entity_id=$1 AND id=$4`, []any{in.ClientID, c.UserID, id}, server.RequestID(r))
	}
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusOK, raw)
}

func (a *API) cards(w http.ResponseWriter, r *http.Request, c auth.Claims, bank string, tail []string) {
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			a.listCards(w, r, bank)
			return
		}
		if r.Method == http.MethodPost && issuerOperator(c) {
			a.issue(w, r, c, bank)
			return
		}
		if r.Method == http.MethodPost {
			a.forbidden(w, r)
			return
		}
		method(w, r, http.MethodGet, http.MethodPost)
		return
	}
	bits := strings.Split(tail[0], ":")
	if !uuid(bits[0]) {
		a.notFound(w, r)
		return
	}
	id := bits[0]
	if len(tail) == 2 && tail[1] == "history" && r.Method == http.MethodGet {
		a.history(w, r, bank, id, true)
		return
	}
	if len(tail) == 2 && tail[1] == "operations" && r.Method == http.MethodGet {
		a.history(w, r, bank, id, false)
		return
	}
	if len(bits) == 1 && r.Method == http.MethodGet {
		raw, err := a.card(r.Context(), bank, id)
		if err != nil {
			a.notFound(w, r)
			return
		}
		server.WriteJSON(w, http.StatusOK, raw)
		return
	}
	if len(bits) == 2 && r.Method == http.MethodPost {
		if bits[1] == "replace" && issuerOperator(c) {
			a.command(w, r, c, bank, id, "replace")
			return
		}
		if (bits[1] == "activate" || bits[1] == "suspend" || bits[1] == "resume" || bits[1] == "close") && canLifecycle(c) {
			a.command(w, r, c, bank, id, bits[1])
			return
		}
		a.forbidden(w, r)
		return
	}
	a.notFound(w, r)
}

func (a *API) listCards(w http.ResponseWriter, r *http.Request, bank string) {
	q := r.URL.Query()
	args := []any{bank}
	where := []string{"entity_id=$1"}
	for _, p := range []string{"client_id", "account_reference_id", "product_id"} {
		if v := q.Get(p); v != "" {
			if !uuid(v) {
				a.invalid(w, r, "Invalid card filter.")
				return
			}
			args = append(args, v)
			where = append(where, p+"=$"+strconv.Itoa(len(args)))
		}
	}
	if status := q.Get("status"); status != "" {
		if !cardStatus(status) {
			a.invalid(w, r, "Invalid card status.")
			return
		}
		args = append(args, status)
		where = append(where, "status=$"+strconv.Itoa(len(args)))
	}
	raws, err := a.list(r.Context(), bank, "cards", "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE "+strings.Join(where, " AND ")+" ORDER BY created_at DESC,id DESC", args[1:])
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.collection(w, r, "cards", bank, raws)
}

func (a *API) issue(w http.ResponseWriter, r *http.Request, c auth.Claims, bank string) {
	var in struct {
		ClientID  string `json:"client_id"`
		AccountID string `json:"account_reference_id"`
		ProductID string `json:"product_id"`
		Reason    string `json:"reason"`
	}
	if !decode(w, r, &in) || !uuid(in.ClientID) || !uuid(in.AccountID) || !uuid(in.ProductID) || strings.TrimSpace(in.Reason) == "" {
		a.invalid(w, r, "client_id, account_reference_id, product_id, and reason are required.")
		return
	}
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	body, _ := normalized(in)
	raw, err := a.issueCard(r.Context(), c, bank, key, body, in, server.RequestID(r))
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusCreated, raw)
}
func (a *API) command(w http.ResponseWriter, r *http.Request, c auth.Claims, bank, id, action string) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) || strings.TrimSpace(in.Reason) == "" {
		a.invalid(w, r, "reason is required.")
		return
	}
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	body, _ := normalized(in)
	raw, status, err := a.cardCommand(r.Context(), c, bank, id, action, key, body, in.Reason, server.RequestID(r))
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, status, raw)
}
func (a *API) history(w http.ResponseWriter, r *http.Request, bank, id string, history bool) {
	query := "SELECT id::text,action,status,reason,created_at,completed_at FROM bank.card_operations WHERE entity_id=$1 AND card_id=$2 ORDER BY created_at DESC,id DESC"
	if history {
		query = "SELECT id::text,previous_status,new_status,reason,created_at FROM bank.card_status_history WHERE entity_id=$1 AND card_id=$2 ORDER BY created_at DESC,id DESC"
	}
	raws, err := a.list(r.Context(), bank, "history", query, []any{id})
	if err != nil {
		a.handle(w, r, err)
		return
	}
	a.collection(w, r, map[bool]string{true: "card-history", false: "card-operations"}[history], bank, raws)
}

func (a *API) expiry(w http.ResponseWriter, r *http.Request, c auth.Claims, bank string, tail []string) {
	if !issuerOperator(c) || len(tail) != 3 || !uuid(tail[0]) || tail[1] != "items" {
		a.notFound(w, r)
		return
	}
	bits := strings.Split(tail[2], ":")
	if len(bits) != 2 || !uuid(bits[0]) || bits[1] != "retry" || r.Method != http.MethodPost {
		a.notFound(w, r)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) || strings.TrimSpace(in.Reason) == "" {
		a.invalid(w, r, "reason is required.")
		return
	}
	key, ok := idempotency(w, r)
	if !ok {
		return
	}
	body, _ := normalized(in)
	err := a.retryExpiry(r.Context(), c, bank, tail[0], bits[0], key, body, in.Reason, server.RequestID(r))
	if err != nil {
		a.handle(w, r, err)
		return
	}
	server.WriteJSON(w, http.StatusAccepted, map[string]any{"id": bits[0], "status": "pending"})
}

// Database helpers. Every shard helper starts its own transaction and sets tenant context first.
func (a *API) tenantTx(ctx context.Context, bank string) (pgx.Tx, error) {
	tx, err := a.shard.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('app.entity_id',$1,true)", bank); err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func (a *API) entity(ctx context.Context, id string) (json.RawMessage, error) {
	return a.one(ctx, id, "SELECT id::text,bank_reference,name,status,created_at,updated_at FROM bank.entities WHERE id=$1", id)
}
func (a *API) one(ctx context.Context, bank, query string, args ...any) (json.RawMessage, error) {
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var raw string
	err = tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM ("+query+") q", args...).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
func (a *API) list(ctx context.Context, bank, kind, query string, args []any) ([]json.RawMessage, error) {
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	all := append([]any{bank}, args...)
	rows, err := tx.Query(ctx, "SELECT row_to_json(q)::text FROM ("+query+") q", all...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]json.RawMessage, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

type apiError struct {
	status       int
	code, detail string
	err          error
}

func (e *apiError) Error() string { return e.detail }
func problem(status int, code, detail string) error {
	return &apiError{status: status, code: code, detail: detail}
}
func (a *API) handle(w http.ResponseWriter, r *http.Request, err error) {
	var e *apiError
	if errors.As(err, &e) {
		server.WriteProblem(w, r, e.status, e.code, e.detail)
		return
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		if pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514" || pg.Code == "P0001" {
			server.WriteProblem(w, r, http.StatusConflict, "conflict", "The request conflicts with the current resource state.")
			return
		}
	}
	a.internal(w, r)
}
func (a *API) invalid(w http.ResponseWriter, r *http.Request, d string) {
	server.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", d)
}
func (a *API) forbidden(w http.ResponseWriter, r *http.Request) {
	server.WriteProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
}
func (a *API) notFound(w http.ResponseWriter, r *http.Request) {
	server.WriteProblem(w, r, http.StatusNotFound, "not_found", "The requested resource does not exist.")
}
func (a *API) internal(w http.ResponseWriter, r *http.Request) {
	server.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
}
func method(w http.ResponseWriter, r *http.Request, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	server.WriteProblem(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "The request method is not allowed for this endpoint.")
}
func uuid(v string) bool {
	if len(v) != 36 || strings.ToLower(v) != v {
		return false
	}
	for i, c := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func cardStatus(v string) bool {
	for _, s := range []string{"pending", "issued", "active", "suspended", "closed", "expired"} {
		if s == v {
			return true
		}
	}
	return false
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func object(v json.RawMessage) bool {
	if len(v) == 0 {
		return false
	}
	var x map[string]any
	return json.Unmarshal(v, &x) == nil && x != nil
}
func objectOrNil(v json.RawMessage) bool { return v == nil || object(v) }
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return false
	}
	return errors.Is(d.Decode(&struct{}{}), io.EOF)
}
func normalized(v any) ([]byte, error) { return json.Marshal(v) }
func idempotency(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 1 || len(key) > 256 {
		server.WriteProblem(w, r, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must contain 1 to 256 characters.")
		return "", false
	}
	return key, true
}

func (a *API) allowedBank(c auth.Claims, bank string) bool {
	if issuer(c) {
		return a.activeBank(context.Background(), bank)
	}
	return c.EntityID == bank && a.activeBank(context.Background(), bank)
}
func (a *API) activeBank(ctx context.Context, bank string) bool {
	var n int
	err := a.routing.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries WHERE entity_id=$1 AND shard_id=$2 AND placement_status='active'", bank, a.shardID).Scan(&n)
	return err == nil && n == 1
}
func (a *API) activeOrProvisioning(ctx context.Context, bank string) bool {
	var n int
	err := a.routing.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries WHERE entity_id=$1 AND shard_id=$2 AND placement_status IN ('active','paused','provisioning')", bank, a.shardID).Scan(&n)
	return err == nil && n == 1
}

func fingerprint(b []byte) []byte { s := sha256.Sum256(b); return s[:] }
func (a *API) centralIdem(ctx context.Context, tx pgx.Tx, scope, key string, body []byte) (json.RawMessage, bool, error) {
	fp := fingerprint(body)
	var old []byte
	var expires time.Time
	var response []byte
	var status int
	err := tx.QueryRow(ctx, "SELECT request_fingerprint,expires_at,response_body,response_status FROM control.command_idempotency_records WHERE operation_scope=$1 AND idempotency_key=$2 FOR UPDATE", scope, key).Scan(&old, &expires, &response, &status)
	if err == nil {
		if a.now().UTC().Before(expires) {
			if !hmac.Equal(old, fp) {
				return nil, false, problem(http.StatusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
			}
			return response, true, nil
		}
		_, err = tx.Exec(ctx, "UPDATE control.command_idempotency_records SET request_fingerprint=$3,response_status=202,response_body='{}',result_reference=NULL,expires_at=$4 WHERE operation_scope=$1 AND idempotency_key=$2", scope, key, fp, a.now().UTC().Add(replayLifetime))
		return nil, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO control.command_idempotency_records(operation_scope,idempotency_key,request_fingerprint,response_status,response_body,expires_at) VALUES ($1,$2,$3,202,'{}',$4)", scope, key, fp, a.now().UTC().Add(replayLifetime))
	return nil, false, err
}
func finishCentral(ctx context.Context, tx pgx.Tx, scope, key string, status int, raw json.RawMessage, id string) error {
	_, err := tx.Exec(ctx, "UPDATE control.command_idempotency_records SET response_status=$3,response_body=$4,result_reference=$5 WHERE operation_scope=$1 AND idempotency_key=$2", scope, key, status, raw, nullID(id))
	return err
}
func nullID(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (a *API) shardIdem(ctx context.Context, tx pgx.Tx, bank, scope, key string, body []byte) (json.RawMessage, int, bool, error) {
	fp := fingerprint(body)
	var old, response []byte
	var expires time.Time
	var status *int
	err := tx.QueryRow(ctx, "SELECT request_fingerprint,expires_at,response_body,response_status FROM bank.idempotency_records WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3 FOR UPDATE", bank, scope, key).Scan(&old, &expires, &response, &status)
	if err == nil {
		if a.now().UTC().Before(expires) {
			if !hmac.Equal(old, fp) {
				return nil, 0, false, problem(http.StatusConflict, "idempotency_conflict", "Idempotency-Key was previously used for a different request.")
			}
			return response, *status, true, nil
		}
		_, err = tx.Exec(ctx, "UPDATE bank.idempotency_records SET request_fingerprint=$4,response_status=202,response_body='{}',result_reference=NULL,status='processing',expires_at=$5,updated_by=$6 WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3", bank, scope, key, fp, a.now().UTC().Add(replayLifetime), "00000000-0000-4000-8000-000000000000")
		return nil, 0, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, false, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.idempotency_records(entity_id,operation_scope,idempotency_key,request_fingerprint,response_status,response_body,expires_at,created_by,updated_by) VALUES ($1,$2,$3,$4,202,'{}',$5,$6,$6)", bank, scope, key, fp, a.now().UTC().Add(replayLifetime), "00000000-0000-4000-8000-000000000000")
	return nil, 0, false, err
}
func finishShard(ctx context.Context, tx pgx.Tx, bank, scope, key string, status int, raw json.RawMessage, result, actor string) error {
	_, err := tx.Exec(ctx, "UPDATE bank.idempotency_records SET status='succeeded',response_status=$4,response_body=$5,result_reference=$6,updated_by=$7 WHERE entity_id=$1 AND operation_scope=$2 AND idempotency_key=$3", bank, scope, key, status, raw, nullID(result), actor)
	return err
}

func (a *API) provisionBank(ctx context.Context, c auth.Claims, key string, body []byte, ref, name, request string) (int, json.RawMessage, error) {
	// The control transaction deliberately commits first in a non-routable state.
	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "banks.create.actor." + c.UserID
	replay, found, err := a.centralIdem(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	var id string
	if found {
		var saved map[string]any
		_ = json.Unmarshal(replay, &saved)
		id, _ = saved["id"].(string)
		if id == "" {
			return http.StatusAccepted, replay, nil
		}
		if _, provisioning := saved["provisioning"]; !provisioning {
			return http.StatusCreated, replay, nil
		}
	} else {
		id, newErr := newUUID()
		if newErr != nil {
			return 0, nil, newErr
		}
		if _, err = tx.Exec(ctx, "INSERT INTO control.bank_routing_entries(entity_id,shard_id,placement_status) VALUES ($1,$2,'provisioning')", id, a.shardID); err != nil {
			return 0, nil, err
		}
		provisional, _ := json.Marshal(map[string]any{"id": id, "provisioning": true})
		if err = finishCentral(ctx, tx, scope, key, http.StatusAccepted, provisional, id); err != nil {
			return 0, nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	stx, err := a.tenantTx(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	_, err = stx.Exec(ctx, "INSERT INTO bank.entities(id,bank_reference,name,created_by,updated_by) VALUES ($1,$2,$3,$4,$4) ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name,updated_by=EXCLUDED.updated_by", id, ref, name, c.UserID)
	if err == nil {
		_, err = stx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator','bank_provision','bank',$1,'succeeded',$3)", id, c.UserID, request)
	}
	if err != nil {
		stx.Rollback(ctx)
		return 0, nil, err
	}
	if err = stx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	raw, err := a.entity(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	tx, err = a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "UPDATE control.bank_routing_entries SET placement_status='active',updated_at=clock_timestamp() WHERE entity_id=$1 AND shard_id=$2", id, a.shardID); err != nil {
		return 0, nil, err
	}
	if err = finishCentral(ctx, tx, scope, key, http.StatusCreated, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, raw, nil
}
func (a *API) patchBank(ctx context.Context, c auth.Claims, id, key string, body []byte, in struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}, request string) (int, json.RawMessage, error) {
	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "banks.patch." + id + ".actor." + c.UserID
	replay, found, err := a.centralIdem(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found && string(replay) != "{}" {
		tx.Rollback(ctx)
		return http.StatusOK, replay, nil
	}
	if !a.activeOrProvisioning(ctx, id) {
		return 0, nil, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
	}
	if in.Status != nil {
		placement := "paused"
		if *in.Status == "active" {
			placement = "active"
		}
		if _, err = tx.Exec(ctx, "UPDATE control.bank_routing_entries SET placement_status=$2,updated_at=clock_timestamp() WHERE entity_id=$1", id, placement); err != nil {
			return 0, nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	stx, err := a.tenantTx(ctx, id)
	if err != nil {
		return 0, nil, err
	}
	defer stx.Rollback(ctx)
	_, err = stx.Exec(ctx, "UPDATE bank.entities SET name=COALESCE($2,name),status=COALESCE($3,status),updated_by=$4 WHERE id=$1", id, in.Name, in.Status, c.UserID)
	if err != nil {
		return 0, nil, err
	}
	_, err = stx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator','bank_update','bank',$1,'succeeded',$3)", id, c.UserID, request)
	if err != nil {
		return 0, nil, err
	}
	var raw string
	err = stx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT id::text,bank_reference,name,status,created_at,updated_at FROM bank.entities WHERE id=$1) q", id).Scan(&raw)
	if err != nil {
		return 0, nil, err
	}
	if err = stx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	tx, err = a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	if err = finishCentral(ctx, tx, scope, key, http.StatusOK, json.RawMessage(raw), id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, json.RawMessage(raw), nil
}

func (a *API) loadUser(ctx context.Context, id string) (json.RawMessage, error) {
	var raw string
	err := a.admin.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT id::text,normalized_username AS username,role,entity_id::text,status,created_at,updated_at FROM control.users WHERE id=$1) q", id).Scan(&raw)
	return json.RawMessage(raw), err
}
func (a *API) createUser(ctx context.Context, c auth.Claims, key string, body []byte, in userInput, request string) (int, json.RawMessage, error) {
	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.create.actor." + c.UserID
	replay, found, err := a.centralIdem(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return http.StatusCreated, replay, nil
	}
	if in.EntityID != "" && !a.activeBank(ctx, in.EntityID) {
		return 0, nil, problem(http.StatusBadRequest, "invalid_request", "The bank assignment must be active.")
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return 0, nil, err
	}
	var id string
	err = tx.QueryRow(ctx, "INSERT INTO control.users(normalized_username,password_hash,role,entity_id,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,$5) RETURNING id::text", strings.ToLower(strings.TrimSpace(in.Username)), hash, in.Role, nullID(in.EntityID), c.UserID).Scan(&id)
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,request_id) VALUES ('user_provision','succeeded',$1,$2,$3,$4)", c.UserID, id, nullID(in.EntityID), request)
	if err != nil {
		return 0, nil, err
	}
	raw, err := userTx(ctx, tx, id)
	if err != nil {
		return 0, nil, err
	}
	if err = finishCentral(ctx, tx, scope, key, http.StatusCreated, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, raw, nil
}
func userTx(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, error) {
	var raw string
	err := tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT id::text,normalized_username AS username,role,entity_id::text,status,created_at,updated_at FROM control.users WHERE id=$1) q", id).Scan(&raw)
	return json.RawMessage(raw), err
}
func (a *API) updateUser(ctx context.Context, c auth.Claims, id, key string, body []byte, role, entity, status, request string) (int, json.RawMessage, error) {
	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.update." + id + ".actor." + c.UserID
	if status != "" {
		scope = "users.disable." + id + ".actor." + c.UserID
	}
	replay, found, err := a.centralIdem(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return http.StatusOK, replay, nil
	}
	if entity != "" && !a.activeBank(ctx, entity) {
		return 0, nil, problem(http.StatusBadRequest, "invalid_request", "The bank assignment must be active.")
	}
	if role != "" {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, "UPDATE control.users SET role=$2,entity_id=$3,updated_by=$4 WHERE id=$1", id, role, nullID(entity), c.UserID)
		if err == nil && tag.RowsAffected() != 1 {
			return 0, nil, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
		}
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, "UPDATE control.users SET status=$2,updated_by=$3 WHERE id=$1", id, status, c.UserID)
		if err == nil && tag.RowsAffected() != 1 {
			return 0, nil, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
		}
	}
	if err != nil {
		return 0, nil, err
	}
	raw, err := userTx(ctx, tx, id)
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,entity_id,request_id) VALUES ($1,'succeeded',$2,$3,NULL,$4)", scope, c.UserID, id, request)
	if err != nil {
		return 0, nil, err
	}
	if err = finishCentral(ctx, tx, scope, key, http.StatusOK, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, raw, nil
}
func (a *API) setPassword(ctx context.Context, c auth.Claims, id, key string, body []byte, password, request string) (int, json.RawMessage, error) {
	tx, err := a.admin.Begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback(ctx)
	scope := "users.set_password." + id + ".actor." + c.UserID
	replay, found, err := a.centralIdem(ctx, tx, scope, key, body)
	if err != nil {
		return 0, nil, err
	}
	if found {
		return http.StatusOK, replay, nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, nil, err
	}
	tag, err := tx.Exec(ctx, "UPDATE control.users SET password_hash=$2,updated_by=$3 WHERE id=$1", id, hash, c.UserID)
	if err != nil {
		return 0, nil, err
	}
	if tag.RowsAffected() != 1 {
		return 0, nil, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
	}
	raw, err := userTx(ctx, tx, id)
	if err != nil {
		return 0, nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO control.authentication_audit_events(event_type,outcome,actor_user_id,subject_user_id,request_id) VALUES ('user_password_reset','succeeded',$1,$2,$3)", c.UserID, id, request)
	if err != nil {
		return 0, nil, err
	}
	if err = finishCentral(ctx, tx, scope, key, http.StatusOK, raw, id); err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, raw, nil
}

func (a *API) mutateRef(ctx context.Context, c auth.Claims, bank, kind, key string, input any, query string, args []any, request string) (json.RawMessage, error) {
	body, err := normalized(input)
	if err != nil {
		return nil, err
	}
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := kind + ".create.actor." + c.UserID
	replay, status, found, err := a.shardIdem(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	all := append([]any{bank}, args...)
	all = append(all, c.UserID)
	var id string
	if err = tx.QueryRow(ctx, query, all...).Scan(&id); err != nil {
		return nil, err
	}
	table, fields := refSQL(kind)
	var raw string
	if err = tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT "+fields+" FROM "+table+" WHERE entity_id=$1 AND id=$2) q", bank, id).Scan(&raw); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator',$3,$4,$5,'succeeded',$6)", bank, c.UserID, kind+".create", kind, id, request); err != nil {
		return nil, err
	}
	if err = finishShard(ctx, tx, bank, scope, key, http.StatusCreated, json.RawMessage(raw), id, c.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	_ = status
	return json.RawMessage(raw), nil
}
func (a *API) patchRef(ctx context.Context, c auth.Claims, bank, kind, id, key string, input any, query string, args []any, request string) (json.RawMessage, error) {
	body, err := normalized(input)
	if err != nil {
		return nil, err
	}
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := kind + ".patch." + id + ".actor." + c.UserID
	replay, _, found, err := a.shardIdem(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	all := append([]any{bank}, args...)
	tag, err := tx.Exec(ctx, query, all...)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
	}
	table, fields := refSQL(kind)
	var raw string
	if err = tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT "+fields+" FROM "+table+" WHERE entity_id=$1 AND id=$2) q", bank, id).Scan(&raw); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,'issuer_operator',$3,$4,$5,'succeeded',$6)", bank, c.UserID, kind+".patch", kind, id, request); err != nil {
		return nil, err
	}
	if err = finishShard(ctx, tx, bank, scope, key, http.StatusOK, json.RawMessage(raw), id, c.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func (a *API) card(ctx context.Context, bank, id string) (json.RawMessage, error) {
	return a.one(ctx, bank, "SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND id=$2", id)
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b)
	return s[0:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}
func credential() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "local_" + base64.RawURLEncoding.EncodeToString(b), nil
}
func actorEntity(c auth.Claims) any {
	if c.Role == "bank_operator" {
		return c.EntityID
	}
	return nil
}
func operationJSON(ctx context.Context, tx pgx.Tx, bank, id string) (json.RawMessage, error) {
	var raw string
	err := tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT id::text,card_id::text,action,status,reason,created_at,completed_at FROM bank.card_operations WHERE entity_id=$1 AND id=$2) q", bank, id).Scan(&raw)
	return json.RawMessage(raw), err
}
func combined(card, op json.RawMessage) (json.RawMessage, error) {
	var x, y any
	if err := json.Unmarshal(card, &x); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(op, &y); err != nil {
		return nil, err
	}
	b, err := json.Marshal(map[string]any{"card": x, "operation": y})
	return b, err
}
func (a *API) issueCard(ctx context.Context, c auth.Claims, bank, key string, body []byte, in struct {
	ClientID  string `json:"client_id"`
	AccountID string `json:"account_reference_id"`
	ProductID string `json:"product_id"`
	Reason    string `json:"reason"`
}, request string) (json.RawMessage, error) {
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	scope := "cards.issue.actor." + c.UserID
	replay, _, found, err := a.shardIdem(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, err
	}
	if found {
		return replay, nil
	}
	var valid bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM bank.account_references a JOIN bank.card_products p ON p.entity_id=a.entity_id WHERE a.entity_id=$1 AND a.id=$2 AND a.client_id=$3 AND p.id=$4 AND p.status='active')", bank, in.AccountID, in.ClientID, in.ProductID).Scan(&valid)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, problem(http.StatusUnprocessableEntity, "invalid_reference", "The card references are not active and bank-consistent.")
	}
	cardID, err := newUUID()
	if err != nil {
		return nil, err
	}
	ref, err := credential()
	if err != nil {
		return nil, err
	}
	opID, err := newUUID()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.cards(entity_id,id,client_id,account_reference_id,product_id,status,credential_reference,issued_at,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,'issued',$6,clock_timestamp(),$7,$7)", bank, cardID, in.ClientID, in.AccountID, in.ProductID, ref, c.UserID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,'issue','succeeded',$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp(),$5,$5)", bank, opID, cardID, in.Reason, c.UserID, c.Role, actorEntity(c), request)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,'pending','issued',$4,$5,$6,$7)", bank, cardID, opID, in.Reason, c.UserID, c.Role, actorEntity(c))
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card.issue','card',$5,'succeeded',$6)", bank, c.UserID, c.Role, actorEntity(c), cardID, request)
	if err != nil {
		return nil, err
	}
	card, err := a.cardTx(ctx, tx, bank, cardID)
	if err != nil {
		return nil, err
	}
	op, err := operationJSON(ctx, tx, bank, opID)
	if err != nil {
		return nil, err
	}
	raw, err := combined(card, op)
	if err != nil {
		return nil, err
	}
	if err = finishShard(ctx, tx, bank, scope, key, http.StatusCreated, raw, opID, c.UserID); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}
func (a *API) cardTx(ctx context.Context, tx pgx.Tx, bank, id string) (json.RawMessage, error) {
	var raw string
	err := tx.QueryRow(ctx, "SELECT row_to_json(q)::text FROM (SELECT id::text,client_id::text,account_reference_id::text,product_id::text,status,predecessor_card_id::text,issued_at,activated_at,suspended_at,closed_at,expires_at,version,created_at,updated_at FROM bank.cards WHERE entity_id=$1 AND id=$2) q", bank, id).Scan(&raw)
	return json.RawMessage(raw), err
}

func transition(current, action string) (string, bool, bool) { // target, valid, ignored
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
func (a *API) cardCommand(ctx context.Context, c auth.Claims, bank, id, action, key string, body []byte, reason, request string) (json.RawMessage, int, error) {
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	scope := "cards." + action + "." + id + ".actor." + c.UserID
	replay, status, found, err := a.shardIdem(ctx, tx, bank, scope, key, body)
	if err != nil {
		return nil, 0, err
	}
	if found {
		return replay, status, nil
	}
	var current, client, account, product string
	err = tx.QueryRow(ctx, "SELECT status::text,client_id::text,account_reference_id::text,product_id::text FROM bank.cards WHERE entity_id=$1 AND id=$2 FOR UPDATE", bank, id).Scan(&current, &client, &account, &product)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, problem(http.StatusNotFound, "not_found", "The requested resource does not exist.")
		}
		return nil, 0, err
	}
	if action == "replace" {
		if current != "active" && current != "suspended" {
			return nil, 0, problem(http.StatusUnprocessableEntity, "invalid_card_transition", "The card cannot be replaced in its current status.")
		}
		var pending bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT FROM bank.cards WHERE entity_id=$1 AND predecessor_card_id=$2 AND status='issued')", bank, id).Scan(&pending); err != nil {
			return nil, 0, err
		}
		if pending {
			return nil, 0, problem(http.StatusConflict, "replacement_pending", "The card already has an issued replacement.")
		}
		successor, err := newUUID()
		if err != nil {
			return nil, 0, err
		}
		credentialRef, err := credential()
		if err != nil {
			return nil, 0, err
		}
		opID, err := newUUID()
		if err != nil {
			return nil, 0, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bank.cards(entity_id,id,client_id,account_reference_id,product_id,status,credential_reference,predecessor_card_id,issued_at,created_by,updated_by) VALUES ($1,$2,$3,$4,$5,'issued',$6,$7,clock_timestamp(),$8,$8)", bank, successor, client, account, product, credentialRef, id, c.UserID)
		if err != nil {
			return nil, 0, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,'replace','succeeded',$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp(),$5,$5)", bank, opID, successor, reason, c.UserID, c.Role, actorEntity(c), request)
		if err != nil {
			return nil, 0, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,'pending','issued',$4,$5,$6,$7)", bank, successor, opID, reason, c.UserID, c.Role, actorEntity(c))
		if err != nil {
			return nil, 0, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id) VALUES ($1,$2,$3,$4,'card.replace','card',$5,'succeeded',$6)", bank, c.UserID, c.Role, actorEntity(c), successor, request)
		if err != nil {
			return nil, 0, err
		}
		card, err := a.cardTx(ctx, tx, bank, successor)
		if err != nil {
			return nil, 0, err
		}
		op, err := operationJSON(ctx, tx, bank, opID)
		if err != nil {
			return nil, 0, err
		}
		raw, err := combined(card, op)
		if err != nil {
			return nil, 0, err
		}
		if err = finishShard(ctx, tx, bank, scope, key, http.StatusCreated, raw, opID, c.UserID); err != nil {
			return nil, 0, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, 0, err
		}
		return raw, http.StatusCreated, nil
	}
	target, valid, ignored := transition(current, action)
	if !valid {
		return nil, 0, problem(http.StatusUnprocessableEntity, "invalid_card_transition", "The requested card transition is not allowed.")
	}
	opID, err := newUUID()
	if err != nil {
		return nil, 0, err
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,$4,'succeeded',$5,$6,$7,$8,$9,clock_timestamp(),clock_timestamp(),$6,$6)", bank, opID, id, action, reason, c.UserID, c.Role, actorEntity(c), request)
	if err != nil {
		return nil, 0, err
	}
	if !ignored {
		field := map[string]string{"activate": "activated_at", "suspend": "suspended_at", "close": "closed_at"}[action]
		query := "UPDATE bank.cards SET status=$3,version=version+1,updated_by=$4"
		if field != "" {
			query += "," + field + "=clock_timestamp()"
		}
		query += " WHERE entity_id=$1 AND id=$2"
		_, err = tx.Exec(ctx, query, bank, id, target, c.UserID)
		if err != nil {
			return nil, 0, err
		}
		_, err = tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)", bank, id, opID, current, target, reason, c.UserID, c.Role, actorEntity(c))
		if err != nil {
			return nil, 0, err
		}
		if action == "activate" {
			var predecessor, predecessorStatus string
			err = tx.QueryRow(ctx, "SELECT p.id::text,p.status::text FROM bank.cards successor JOIN bank.cards p ON p.entity_id=successor.entity_id AND p.id=successor.predecessor_card_id WHERE successor.entity_id=$1 AND successor.id=$2 FOR UPDATE", bank, id).Scan(&predecessor, &predecessorStatus)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, 0, err
			}
			if err == nil && (predecessorStatus == "active" || predecessorStatus == "suspended") {
				predecessorOperation, createErr := newUUID()
				if createErr != nil {
					return nil, 0, createErr
				}
				_, err = tx.Exec(ctx, "UPDATE bank.cards SET status='closed',closed_at=clock_timestamp(),version=version+1,updated_by=$3 WHERE entity_id=$1 AND id=$2 AND status IN ('active','suspended')", bank, predecessor, c.UserID)
				if err != nil {
					return nil, 0, err
				}
				_, err = tx.Exec(ctx, "INSERT INTO bank.card_operations(entity_id,id,card_id,action,status,reason,actor_user_id,actor_role,actor_entity_id,request_id,started_at,completed_at,created_by,updated_by) VALUES ($1,$2,$3,'close','succeeded',$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp(),$5,$5)", bank, predecessorOperation, predecessor, reason, c.UserID, c.Role, actorEntity(c), request)
				if err != nil {
					return nil, 0, err
				}
				_, err = tx.Exec(ctx, "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role,actor_entity_id) VALUES ($1,$2,$3,$4,'closed',$5,$6,$7,$8)", bank, predecessor, predecessorOperation, predecessorStatus, reason, c.UserID, c.Role, actorEntity(c))
				if err != nil {
					return nil, 0, err
				}
			}
		}
	}
	outcome := "succeeded"
	if ignored {
		outcome = "succeeded"
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,actor_entity_id,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,$3,$4,$5,'card',$6,$7,$8,$9)", bank, c.UserID, c.Role, actorEntity(c), "card."+action, id, outcome, request, []byte(fmt.Sprintf(`{"ignored":%t}`, ignored)))
	if err != nil {
		return nil, 0, err
	}
	card, err := a.cardTx(ctx, tx, bank, id)
	if err != nil {
		return nil, 0, err
	}
	op, err := operationJSON(ctx, tx, bank, opID)
	if err != nil {
		return nil, 0, err
	}
	raw, err := combined(card, op)
	if err != nil {
		return nil, 0, err
	}
	if err = finishShard(ctx, tx, bank, scope, key, http.StatusOK, raw, opID, c.UserID); err != nil {
		return nil, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return raw, http.StatusOK, nil
}

func (a *API) retryExpiry(ctx context.Context, c auth.Claims, bank, run, item, key string, body []byte, reason, request string) error {
	tx, err := a.tenantTx(ctx, bank)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	scope := "expiry.retry." + run + "." + item + ".actor." + c.UserID
	_, _, found, err := a.shardIdem(ctx, tx, bank, scope, key, body)
	if err != nil {
		return err
	}
	if found {
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, "UPDATE bank.card_expiry_run_items i SET status='pending',manual_retry_count=manual_retry_count+1,manual_retry_by=$4,manual_retry_at=clock_timestamp(),next_attempt_at=clock_timestamp() WHERE i.entity_id=$1 AND i.expiry_run_id=$2 AND i.id=$3 AND i.status='manual_retry_required' AND EXISTS (SELECT FROM bank.cards c WHERE c.entity_id=i.entity_id AND c.id=i.card_id AND c.status<>'expired')", bank, run, item, c.UserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return problem(http.StatusConflict, "expiry_item_not_retryable", "The expiry item is not eligible for manual retry.")
	}
	_, err = tx.Exec(ctx, "INSERT INTO bank.audit_events(entity_id,actor_user_id,actor_role,action,resource_type,resource_id,outcome,request_id,details) VALUES ($1,$2,'issuer_operator','expiry_retry','expiry_item',$3,'succeeded',$4,$5)", bank, c.UserID, item, request, []byte(`{"reason":"manual_retry"}`))
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"id": item, "status": "pending"})
	if err = finishShard(ctx, tx, bank, scope, key, http.StatusAccepted, raw, item, c.UserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type cursor struct {
	Kind    string `json:"k"`
	Bank    string `json:"b"`
	Filter  string `json:"f"`
	Created string `json:"c"`
	ID      string `json:"i"`
}

func (a *API) cursorEncode(v cursor) string {
	b, _ := json.Marshal(v)
	m := hmac.New(sha256.New, a.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (a *API) cursorDecode(token, kind, bank, filter string) (cursor, error) {
	p := strings.Split(token, ".")
	if len(p) != 2 {
		return cursor{}, errors.New("invalid cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(p[0])
	if err != nil {
		return cursor{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(p[1])
	if err != nil {
		return cursor{}, err
	}
	m := hmac.New(sha256.New, a.key)
	m.Write(b)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return cursor{}, errors.New("invalid cursor")
	}
	var v cursor
	if json.Unmarshal(b, &v) != nil || v.Kind != kind || v.Bank != bank || v.Filter != filter || !uuid(v.ID) {
		return cursor{}, errors.New("invalid cursor")
	}
	return v, nil
}
func canonicalQuery(q url.Values) string {
	copy := url.Values{}
	for k, v := range q {
		if k != "cursor" && k != "page_size" {
			copy[k] = append([]string(nil), v...)
		}
	}
	return copy.Encode()
}

// collection applies the documented cursor contract after a deterministic
// creation-time/ID query. The repository still performs tenant filtering in SQL;
// this final slice keeps the cursor representation independent of database types.
func (a *API) collection(w http.ResponseWriter, r *http.Request, kind, bank string, data []json.RawMessage) {
	limit := 50
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			a.invalid(w, r, "page_size must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	filter := canonicalQuery(r.URL.Query())
	start := 0
	if token := r.URL.Query().Get("cursor"); token != "" {
		cursor, err := a.cursorDecode(token, kind, bank, filter)
		if err != nil {
			server.WriteProblem(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid for this collection.")
			return
		}
		cursorTime, err := time.Parse(time.RFC3339Nano, cursor.Created)
		if err != nil {
			server.WriteProblem(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid for this collection.")
			return
		}
		for start < len(data) {
			created, id, ok := collectionKey(data[start])
			if !ok {
				a.internal(w, r)
				return
			}
			createdTime, err := time.Parse(time.RFC3339Nano, created)
			if err != nil {
				a.internal(w, r)
				return
			}
			if createdTime.Before(cursorTime) || (createdTime.Equal(cursorTime) && id < cursor.ID) {
				break
			}
			start++
		}
	}
	end := start + limit
	if end > len(data) {
		end = len(data)
	}
	page := data[start:end]
	var next any
	if end < len(data) && len(page) > 0 {
		created, id, ok := collectionKey(page[len(page)-1])
		if !ok {
			a.internal(w, r)
			return
		}
		next = a.cursorEncode(cursor{Kind: kind, Bank: bank, Filter: filter, Created: created, ID: id})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"data": page, "next_cursor": next})
}
func collectionKey(raw json.RawMessage) (string, string, bool) {
	var v struct {
		ID      string `json:"id"`
		Created string `json:"created_at"`
	}
	if json.Unmarshal(raw, &v) != nil || !uuid(v.ID) || v.Created == "" {
		return "", "", false
	}
	return v.Created, v.ID, true
}
