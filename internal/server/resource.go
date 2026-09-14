package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"card-issuer-api/internal/auth"
)

// BankService is the HTTP-facing workflow boundary for bank-directory actions.
type BankService interface {
	ListBanks(context.Context) ([]json.RawMessage, error)
	GetBank(context.Context, string) (json.RawMessage, error)
	ProvisionBankWorkflow(context.Context, auth.Principal, string, []byte, string, string, string) (int, json.RawMessage, error)
	PatchBankWorkflow(context.Context, auth.Principal, string, string, []byte, *string, *string, string) (int, json.RawMessage, error)
}

// BankAccess owns bank-scoped read authorization separately from individual
// resource workflows.
type BankAccess interface {
	CanReadBank(context.Context, auth.Principal, string) bool
}

// CatalogService is the HTTP-facing workflow boundary for bank reference data.
type CatalogService interface {
	ListReference(context.Context, string, string) ([]json.RawMessage, error)
	GetReference(context.Context, string, string, string) (json.RawMessage, error)
	CreateCardProductWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, string, json.RawMessage, string) (json.RawMessage, error)
	CreateClientWorkflow(context.Context, auth.Principal, string, string, []byte, string, *string, string) (json.RawMessage, error)
	CreateAccountReferenceWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, string) (json.RawMessage, error)
	PatchCardProductWorkflow(context.Context, auth.Principal, string, string, string, []byte, *string, *string, json.RawMessage, string) (json.RawMessage, error)
	PatchClientWorkflow(context.Context, auth.Principal, string, string, string, []byte, *string, string) (json.RawMessage, error)
	PatchAccountReferenceWorkflow(context.Context, auth.Principal, string, string, string, []byte, string, string) (json.RawMessage, error)
}

// CardService is the HTTP-facing workflow boundary for card queries and
// synchronous lifecycle commands.
type CardService interface {
	ListCards(context.Context, string, string, string, string, string) ([]json.RawMessage, error)
	GetCard(context.Context, string, string) (json.RawMessage, error)
	ListCardOperations(context.Context, string, string) ([]json.RawMessage, error)
	ListCardStatusHistory(context.Context, string, string) ([]json.RawMessage, error)
	IssueCardWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, string, string, string) (json.RawMessage, error)
	CardCommandWorkflow(context.Context, auth.Principal, string, string, string, string, []byte, string, string) (json.RawMessage, int, error)
	RetryExpiryWorkflow(context.Context, auth.Principal, string, string, string, string, []byte, string, string) error
}

// BatchService is the HTTP-facing workflow boundary for card-status batches.
type BatchService interface {
	ListCardStatusBatches(context.Context, string) ([]json.RawMessage, error)
	GetCardStatusBatch(context.Context, string, string) (json.RawMessage, error)
	ListCardStatusBatchItems(context.Context, string, string) ([]json.RawMessage, error)
	CreateCardStatusBatchWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, []string, string) (json.RawMessage, error)
	ExecuteCardStatusBatchWorkflow(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
	CancelCardStatusBatchWorkflow(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
	RetryCardStatusBatchWorkflow(context.Context, auth.Principal, string, string, string, []byte, string) (json.RawMessage, error)
}

// StaffService is the HTTP-facing workflow boundary for issuer staff actions.
type StaffService interface {
	ListUsers(context.Context) ([]json.RawMessage, error)
	GetUser(context.Context, string) (json.RawMessage, error)
	CreateUserWorkflow(context.Context, auth.Principal, string, []byte, string, string, string, string, string) (int, json.RawMessage, error)
	UpdateUserWorkflow(context.Context, auth.Principal, string, string, []byte, string, string, string, string) (int, json.RawMessage, error)
	SetUserPasswordWorkflow(context.Context, auth.Principal, string, string, []byte, string, string) (int, json.RawMessage, error)
}

// Dependencies lists the feature-scoped application boundaries used by the
// HTTP handler. Keeping these separate makes composition explicit and lets
// every handler depend only on the workflow it invokes.
type Dependencies struct {
	Bank    BankService
	Access  BankAccess
	Catalog CatalogService
	Card    CardService
	Batch   BatchService
	Staff   StaffService
}

type resourceHandler struct {
	authentication auth.API
	bankService    BankService
	access         BankAccess
	catalog        CatalogService
	card           CardService
	batch          BatchService
	staff          StaffService
	cursorKey      []byte
}

// NewResourceHandler owns routing and authentication for the non-auth public API.
func NewResourceHandler(authentication auth.API, dependencies Dependencies, cursorKey []byte) http.Handler {
	return resourceHandler{
		authentication: authentication,
		bankService:    dependencies.Bank,
		access:         dependencies.Access,
		catalog:        dependencies.Catalog,
		card:           dependencies.Card,
		batch:          dependencies.Batch,
		staff:          dependencies.Staff,
		cursorKey:      append([]byte(nil), cursorKey...),
	}
}

func (h resourceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/")
	switch {
	case path == "banks":
		h.banks(w, r, claims)
	case path == "users":
		h.users(w, r, claims)
	case strings.HasPrefix(path, "users/"):
		h.user(w, r, claims, strings.TrimPrefix(path, "users/"))
	case strings.HasPrefix(path, "banks/"):
		tail := strings.TrimPrefix(path, "banks/")
		if !strings.Contains(tail, "/") {
			h.bank(w, r, claims, tail)
			return
		}
		h.bankResource(w, r, claims, tail)
	default:
		apiNotFound(w, r)
	}
}

func (h resourceHandler) bankResource(w http.ResponseWriter, r *http.Request, claims auth.Claims, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) < 2 || !ValidUUID(parts[0]) {
		apiNotFound(w, r)
		return
	}
	bank := parts[0]
	kind := parts[1]
	if kind == "cards" && r.Method == http.MethodGet {
		h.cardReads(w, r, claims, bank, parts[2:])
		return
	}
	if kind == "cards" && r.Method == http.MethodPost {
		h.cardWrites(w, r, claims, bank, parts[2:])
		return
	}
	if kind == "card-expiry-runs" && r.Method == http.MethodPost {
		h.expiryRetry(w, r, claims, bank, parts[2:])
		return
	}
	if kind == "card-status-batches" {
		h.cardStatusBatches(w, r, claims, bank, parts[2:])
		return
	}
	if kind != "card-products" && kind != "clients" && kind != "account-references" {
		apiNotFound(w, r)
		return
	}
	if !canRead(claims) || !h.access.CanReadBank(r.Context(), claims.Principal(), bank) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if len(parts) == 2 {
		if r.Method == http.MethodPost {
			h.referenceCreate(w, r, claims, bank, kind)
			return
		}
		if r.Method != http.MethodGet {
			RequireMethods(w, r, http.MethodGet, http.MethodPost)
			return
		}
		data, err := h.catalog.ListReference(r.Context(), bank, kind)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, kind, bank, data)
		return
	}
	if len(parts) == 3 && ValidUUID(parts[2]) {
		if r.Method == http.MethodPatch {
			h.referencePatch(w, r, claims, bank, kind, parts[2])
			return
		}
		if r.Method != http.MethodGet {
			apiNotFound(w, r)
			return
		}
		raw, err := h.catalog.GetReference(r.Context(), bank, kind, parts[2])
		if err != nil {
			apiNotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	apiNotFound(w, r)
}

func canRead(claims auth.Claims) bool {
	return claims.Role == "issuer_operator" || claims.Role == "issuer_readonly" || claims.Role == "bank_operator" || claims.Role == "bank_readonly"
}

type pageCursor struct {
	Kind    string `json:"k"`
	Bank    string `json:"b"`
	Filter  string `json:"f"`
	Created string `json:"c"`
	ID      string `json:"i"`
}

func (h resourceHandler) collection(w http.ResponseWriter, r *http.Request, kind, bank string, data []json.RawMessage) {
	limit := 50
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 200.")
			return
		}
		limit = parsed
	}
	filter := canonicalQuery(r.URL.Query())
	start := 0
	if token := r.URL.Query().Get("cursor"); token != "" {
		cursor, err := h.cursorDecode(token, kind, bank, filter)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid for this collection.")
			return
		}
		cursorTime, err := time.Parse(time.RFC3339Nano, cursor.Created)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid for this collection.")
			return
		}
		for start < len(data) {
			created, id, ok := collectionKey(data[start])
			if !ok {
				writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
				return
			}
			createdTime, err := time.Parse(time.RFC3339Nano, created)
			if err != nil {
				writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
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
			writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
			return
		}
		next = h.cursorEncode(pageCursor{Kind: kind, Bank: bank, Filter: filter, Created: created, ID: id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": page, "next_cursor": next})
}

func (h resourceHandler) cursorEncode(value pageCursor) string {
	payload, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, h.cursorKey)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (h resourceHandler) cursorDecode(token, kind, bank, filter string) (pageCursor, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return pageCursor{}, errors.New("invalid cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return pageCursor{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return pageCursor{}, err
	}
	mac := hmac.New(sha256.New, h.cursorKey)
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return pageCursor{}, errors.New("invalid cursor")
	}
	var value pageCursor
	if json.Unmarshal(payload, &value) != nil || value.Kind != kind || value.Bank != bank || value.Filter != filter || !ValidUUID(value.ID) {
		return pageCursor{}, errors.New("invalid cursor")
	}
	return value, nil
}
func canonicalQuery(query url.Values) string {
	copy := url.Values{}
	for key, values := range query {
		if key != "cursor" && key != "page_size" {
			copy[key] = append([]string(nil), values...)
		}
	}
	return copy.Encode()
}
func collectionKey(raw json.RawMessage) (string, string, bool) {
	var value struct {
		ID      string `json:"id"`
		Created string `json:"created_at"`
	}
	if json.Unmarshal(raw, &value) != nil || !ValidUUID(value.ID) || value.Created == "" {
		return "", "", false
	}
	return value.Created, value.ID, true
}

func (h resourceHandler) writeWorkflowError(w http.ResponseWriter, r *http.Request, err error) {
	var problemError interface{ HTTPProblem() (int, string, string) }
	if errors.As(err, &problemError) {
		status, code, detail := problemError.HTTPProblem()
		writeProblem(w, r, status, code, detail)
		return
	}
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) && (databaseError.SQLState() == "23505" || databaseError.SQLState() == "23503" || databaseError.SQLState() == "23514" || databaseError.SQLState() == "P0001") {
		writeProblem(w, r, http.StatusConflict, "conflict", "The request conflicts with the current resource state.")
		return
	}
	writeProblem(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
}

func (h resourceHandler) authenticate(w http.ResponseWriter, r *http.Request) (auth.Claims, bool) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	claims, err := h.authentication.ValidateAccess(parts[1])
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="card-issuer-api", error="invalid_token"`)
		writeProblem(w, r, http.StatusUnauthorized, "invalid_access_token", "A valid bearer access token is required.")
		return auth.Claims{}, false
	}
	return claims, true
}
