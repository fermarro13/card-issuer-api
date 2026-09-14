package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"card-issuer-api/internal/auth"
)

func (h resourceHandler) banks(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, err := h.bankService.ListBanks(r.Context())
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, "banks", "directory", data)
	case http.MethodPost:
		var in struct {
			BankReference string `json:"bank_reference"`
			Name          string `json:"name"`
		}
		if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.BankReference) == "" || strings.TrimSpace(in.Name) == "" {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "bank_reference and name are required.")
			return
		}
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		body, _ := json.Marshal(in)
		status, result, err := h.bankService.ProvisionBankWorkflow(r.Context(), claims.Principal(), key, body, in.BankReference, in.Name, RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, status, result)
	default:
		RequireMethods(w, r, http.MethodGet, http.MethodPost)
	}
}

func (h resourceHandler) bank(w http.ResponseWriter, r *http.Request, claims auth.Claims, id string) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if !ValidUUID(id) {
		apiNotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		raw, err := h.bankService.GetBank(r.Context(), id)
		if err != nil {
			apiNotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	if r.Method != http.MethodPatch {
		RequireMethods(w, r, http.MethodGet, http.MethodPatch)
		return
	}
	var in struct {
		Name   *string `json:"name"`
		Status *string `json:"status"`
	}
	if !DecodeRequest(w, r, &in) || (in.Name == nil && in.Status == nil) || (in.Name != nil && strings.TrimSpace(*in.Name) == "") || (in.Status != nil && *in.Status != "active" && *in.Status != "inactive") {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "A valid name or status is required.")
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(in)
	status, result, err := h.bankService.PatchBankWorkflow(r.Context(), claims.Principal(), id, key, body, in.Name, in.Status, RequestID(r))
	if err != nil {
		h.writeWorkflowError(w, r, err)
		return
	}
	writeJSON(w, status, result)
}
