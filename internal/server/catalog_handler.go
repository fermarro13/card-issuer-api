package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"card-issuer-api/internal/auth"
)

func (h resourceHandler) referenceCreate(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank, kind string) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	requestID := RequestID(r)
	switch kind {
	case "card-products":
		var in struct {
			ProductCode   string          `json:"product_code"`
			Name          string          `json:"name"`
			Status        string          `json:"status"`
			Configuration json.RawMessage `json:"configuration"`
		}
		if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.ProductCode) == "" || strings.TrimSpace(in.Name) == "" || (in.Status != "" && in.Status != "active" && in.Status != "inactive") || !JSONObject(in.Configuration) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "product_code, name, optional status, and object configuration are required.")
			return
		}
		if in.Status == "" {
			in.Status = "active"
		}
		if in.Configuration == nil {
			in.Configuration = json.RawMessage(`{}`)
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.CreateCardProductWorkflow(r.Context(), claims.Principal(), bank, key, body, in.ProductCode, in.Name, in.Status, in.Configuration, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, raw)
	case "clients":
		var in struct {
			External string  `json:"external_client_ref"`
			Display  *string `json:"display_name"`
		}
		if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.External) == "" {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "external_client_ref is required.")
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.CreateClientWorkflow(r.Context(), claims.Principal(), bank, key, body, in.External, in.Display, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, raw)
	case "account-references":
		var in struct {
			ClientID string `json:"client_id"`
			External string `json:"external_account_ref"`
		}
		if !DecodeRequest(w, r, &in) || !ValidUUID(in.ClientID) || strings.TrimSpace(in.External) == "" {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "client_id and external_account_ref are required.")
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.CreateAccountReferenceWorkflow(r.Context(), claims.Principal(), bank, key, body, in.ClientID, in.External, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, raw)
	}
}

func (h resourceHandler) referencePatch(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank, kind, id string) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	requestID := RequestID(r)
	switch kind {
	case "card-products":
		var in struct {
			Name          *string         `json:"name"`
			Status        *string         `json:"status"`
			Configuration json.RawMessage `json:"configuration"`
		}
		if !DecodeRequest(w, r, &in) || (in.Name == nil && in.Status == nil && in.Configuration == nil) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Status != nil && *in.Status != "active" && *in.Status != "inactive") || !JSONObjectOrNil(in.Configuration) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "A valid name, status, or object configuration is required.")
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.PatchCardProductWorkflow(r.Context(), claims.Principal(), bank, id, key, body, in.Name, in.Status, in.Configuration, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, raw)
	case "clients":
		var in struct {
			Display *string `json:"display_name"`
		}
		if !DecodeRequest(w, r, &in) || in.Display == nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "display_name is required.")
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.PatchClientWorkflow(r.Context(), claims.Principal(), bank, id, key, body, in.Display, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, raw)
	case "account-references":
		var in struct {
			ClientID string `json:"client_id"`
		}
		if !DecodeRequest(w, r, &in) || !ValidUUID(in.ClientID) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "client_id is required.")
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.catalog.PatchAccountReferenceWorkflow(r.Context(), claims.Principal(), bank, id, key, body, in.ClientID, requestID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, raw)
	}
}
