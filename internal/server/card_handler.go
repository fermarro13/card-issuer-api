package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"card-issuer-api/internal/auth"
)

func (h resourceHandler) cardReads(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank string, tail []string) {
	if !canRead(claims) || !h.access.CanReadBank(r.Context(), claims.Principal(), bank) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if len(tail) == 0 {
		query := r.URL.Query()
		filter := struct{ ClientID, AccountID, ProductID, Status string }{ClientID: query.Get("client_id"), AccountID: query.Get("account_reference_id"), ProductID: query.Get("product_id"), Status: query.Get("status")}
		for _, value := range []string{filter.ClientID, filter.AccountID, filter.ProductID} {
			if value != "" && !ValidUUID(value) {
				writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid card filter.")
				return
			}
		}
		if filter.Status != "" && !ValidCardStatus(filter.Status) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "Invalid card status.")
			return
		}
		data, err := h.card.ListCards(r.Context(), bank, filter.ClientID, filter.AccountID, filter.ProductID, filter.Status)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, "cards", bank, data)
		return
	}
	if !ValidUUID(tail[0]) {
		apiNotFound(w, r)
		return
	}
	cardID := tail[0]
	if len(tail) == 1 {
		raw, err := h.card.GetCard(r.Context(), bank, cardID)
		if err != nil {
			apiNotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	if len(tail) == 2 && tail[1] == "operations" {
		data, err := h.card.ListCardOperations(r.Context(), bank, cardID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, "card-operations", bank, data)
		return
	}
	if len(tail) == 2 && tail[1] == "history" {
		data, err := h.card.ListCardStatusHistory(r.Context(), bank, cardID)
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, "card-history", bank, data)
		return
	}
	apiNotFound(w, r)
}

func (h resourceHandler) cardWrites(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank string, tail []string) {
	if !canRead(claims) || !h.access.CanReadBank(r.Context(), claims.Principal(), bank) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if len(tail) == 0 {
		if claims.Role != "issuer_operator" && claims.Role != "bank_operator" {
			writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
			return
		}
		var in struct {
			ClientID  string `json:"client_id"`
			AccountID string `json:"account_reference_id"`
			ProductID string `json:"product_id"`
			Reason    string `json:"reason"`
		}
		if !DecodeRequest(w, r, &in) || !ValidUUID(in.ClientID) || !ValidUUID(in.AccountID) || !ValidUUID(in.ProductID) || strings.TrimSpace(in.Reason) == "" {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "client_id, account_reference_id, product_id, and reason are required.")
			return
		}
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		body, _ := json.Marshal(in)
		raw, err := h.card.IssueCardWorkflow(r.Context(), claims.Principal(), bank, key, body, in.ClientID, in.AccountID, in.ProductID, in.Reason, RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, raw)
		return
	}
	if len(tail) != 1 {
		apiNotFound(w, r)
		return
	}
	bits := strings.Split(tail[0], ":")
	if len(bits) != 2 || !ValidUUID(bits[0]) {
		apiNotFound(w, r)
		return
	}
	action := bits[1]
	if action == "replace" {
		if claims.Role != "issuer_operator" && claims.Role != "bank_operator" {
			writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
			return
		}
	} else if action != "activate" && action != "suspend" && action != "resume" && action != "close" || (claims.Role != "issuer_operator" && claims.Role != "bank_operator") {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.Reason) == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "reason is required.")
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(in)
	raw, status, err := h.card.CardCommandWorkflow(r.Context(), claims.Principal(), bank, bits[0], action, key, body, in.Reason, RequestID(r))
	if err != nil {
		h.writeWorkflowError(w, r, err)
		return
	}
	writeJSON(w, status, raw)
}

func (h resourceHandler) expiryRetry(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank string, tail []string) {
	if claims.Role != "issuer_operator" || !h.access.CanReadBank(r.Context(), claims.Principal(), bank) || len(tail) != 2 || !ValidUUID(tail[0]) {
		apiNotFound(w, r)
		return
	}
	bits := strings.Split(tail[1], ":")
	if len(bits) != 2 || !ValidUUID(bits[0]) || bits[1] != "retry" {
		apiNotFound(w, r)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.Reason) == "" {
		writeProblem(w, r, http.StatusBadRequest, "invalid_request", "reason is required.")
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(in)
	if err := h.card.RetryExpiryWorkflow(r.Context(), claims.Principal(), bank, tail[0], bits[0], key, body, in.Reason, RequestID(r)); err != nil {
		h.writeWorkflowError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": bits[0], "status": "pending"})
}
