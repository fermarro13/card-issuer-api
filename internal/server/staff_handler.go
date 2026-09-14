package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"card-issuer-api/internal/auth"
)

func (h resourceHandler) users(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		data, err := h.staff.ListUsers(r.Context())
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		h.collection(w, r, "users", "control", data)
	case http.MethodPost:
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Role     string `json:"role"`
			EntityID string `json:"entity_id"`
		}
		if !DecodeRequest(w, r, &in) || strings.TrimSpace(in.Username) == "" || !validRole(in.Role) || ((in.Role == "bank_operator" || in.Role == "bank_readonly") != ValidUUID(in.EntityID)) || auth.ValidateNewPassword(in.Password) != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "username, password, role, and a valid bank assignment are required.")
			return
		}
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		body, _ := json.Marshal(in)
		status, result, err := h.staff.CreateUserWorkflow(r.Context(), claims.Principal(), key, body, in.Username, in.Password, in.Role, in.EntityID, RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, status, result)
	default:
		RequireMethods(w, r, http.MethodGet, http.MethodPost)
	}
}

func (h resourceHandler) user(w http.ResponseWriter, r *http.Request, claims auth.Claims, tail string) {
	if claims.Role != "issuer_operator" {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "The authenticated user is not permitted to perform this operation.")
		return
	}
	pieces := strings.Split(tail, ":")
	if !ValidUUID(pieces[0]) {
		apiNotFound(w, r)
		return
	}
	id := pieces[0]
	if len(pieces) == 1 && r.Method == http.MethodGet {
		raw, err := h.staff.GetUser(r.Context(), id)
		if err != nil {
			apiNotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, raw)
		return
	}
	if len(pieces) == 1 && r.Method == http.MethodPatch {
		var in struct {
			Role     *string `json:"role"`
			EntityID *string `json:"entity_id"`
		}
		if !DecodeRequest(w, r, &in) || in.Role == nil || !validRole(*in.Role) || ((*in.Role == "bank_operator" || *in.Role == "bank_readonly") != (in.EntityID != nil && ValidUUID(*in.EntityID))) {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "role and its required bank assignment are invalid.")
			return
		}
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		body, _ := json.Marshal(in)
		status, result, err := h.staff.UpdateUserWorkflow(r.Context(), claims.Principal(), id, key, body, *in.Role, stringValue(in.EntityID), "", RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, status, result)
		return
	}
	if len(pieces) == 2 && pieces[1] == "disable" && r.Method == http.MethodPost {
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		status, result, err := h.staff.UpdateUserWorkflow(r.Context(), claims.Principal(), id, key, []byte("{}"), "", "", "disabled", RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, status, result)
		return
	}
	if len(pieces) == 2 && pieces[1] == "set-password" && r.Method == http.MethodPost {
		var in struct {
			Password string `json:"new_password"`
		}
		if !DecodeRequest(w, r, &in) || auth.ValidateNewPassword(in.Password) != nil {
			writeProblem(w, r, http.StatusBadRequest, "invalid_request", "new_password must meet the password policy.")
			return
		}
		key, ok := IdempotencyKey(w, r)
		if !ok {
			return
		}
		body, _ := json.Marshal(in)
		status, result, err := h.staff.SetUserPasswordWorkflow(r.Context(), claims.Principal(), id, key, body, in.Password, RequestID(r))
		if err != nil {
			h.writeWorkflowError(w, r, err)
			return
		}
		writeJSON(w, status, result)
		return
	}
	RequireMethods(w, r, http.MethodGet, http.MethodPatch, http.MethodPost)
}

func validRole(role string) bool {
	return role == "issuer_operator" || role == "issuer_readonly" || role == "bank_operator" || role == "bank_readonly"
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
