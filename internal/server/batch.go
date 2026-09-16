package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"card-issuer-api/internal/auth"
)

func (h resourceHandler) cardStatusBatches(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank string, tail []string) {
	if !canRead(claims) || !h.access.CanReadBank(r.Context(), claims.Principal(), bank) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			data, err := h.batch.ListCardStatusBatches(r.Context(), bank)
			if err != nil {
				h.writeWorkflowError(w, r, err)
				return
			}
			h.collection(w, r, "card-status-batches", bank, data)
		case http.MethodPost:
			h.createCardStatusBatch(w, r, claims, bank)
		default:
			RequireMethods(w, r, http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(tail) == 1 && ValidUUID(tail[0]) && r.Method == http.MethodGet {
		raw, err := h.batch.GetCardStatusBatch(r.Context(), bank, tail[0])
		if err != nil {
			apiNotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	if len(tail) == 2 && ValidUUID(tail[0]) && tail[1] == "items" && r.Method == http.MethodGet {
		data, err := h.batch.ListCardStatusBatchItems(r.Context(), bank, tail[0])
		if err != nil {
			apiNotFound(w, r)
			return
		}
		h.collection(w, r, "card-status-batch-items", bank+":"+tail[0], data)
		return
	}
	if len(tail) == 1 {
		bits := strings.Split(tail[0], ":")
		if len(bits) == 2 && ValidUUID(bits[0]) {
			h.cardStatusBatchCommand(w, r, claims, bank, bits[0], bits[1])
			return
		}
	}
	apiNotFound(w, r)
}

func (h resourceHandler) createCardStatusBatch(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank string) {
	if !canManageCardStatusBatches(claims) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	var input struct {
		TargetStatus string   `json:"target_status"`
		Reason       string   `json:"reason"`
		CardIDs      []string `json:"card_ids"`
	}
	if !DecodeRequest(w, r, &input) {
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(input)
	raw, status, err := h.batch.CreateCardStatusBatchWorkflow(r.Context(), claims.Principal(), bank, key, body, input.TargetStatus, input.Reason, input.CardIDs, RequestID(r))
	if err != nil {
		h.writeWorkflowError(w, r, err)
		return
	}
	writeJSON(w, status, raw)
}

func (h resourceHandler) cardStatusBatchCommand(w http.ResponseWriter, r *http.Request, claims auth.Claims, bank, id, action string) {
	if r.Method != http.MethodPost {
		RequireMethods(w, r, http.MethodPost)
		return
	}
	if !canManageCardStatusBatches(claims) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	if action != "execute" && action != "cancel" && action != "retry" {
		apiNotFound(w, r)
		return
	}
	var input struct{}
	if !DecodeRequest(w, r, &input) {
		return
	}
	key, ok := IdempotencyKey(w, r)
	if !ok {
		return
	}
	body, _ := json.Marshal(input)
	var (
		raw    json.RawMessage
		err    error
		status = http.StatusAccepted
	)
	switch action {
	case "execute":
		raw, err = h.batch.ExecuteCardStatusBatchWorkflow(r.Context(), claims.Principal(), bank, id, key, body, RequestID(r))
	case "cancel":
		raw, err = h.batch.CancelCardStatusBatchWorkflow(r.Context(), claims.Principal(), bank, id, key, body, RequestID(r))
		status = http.StatusOK
	case "retry":
		raw, err = h.batch.RetryCardStatusBatchWorkflow(r.Context(), claims.Principal(), bank, id, key, body, RequestID(r))
		status = http.StatusCreated
	}
	if err != nil {
		h.writeWorkflowError(w, r, err)
		return
	}
	writeJSON(w, status, raw)
}

func canManageCardStatusBatches(claims auth.Claims) bool {
	return claims.Role == "issuer_operator" || claims.Role == "bank_operator"
}
