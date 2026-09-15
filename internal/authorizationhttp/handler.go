// Package authorizationhttp implements the mTLS-only bank authorization boundary.
package authorizationhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	domain "card-issuer-api/internal/domain"
)

type Service interface {
	Verify(context.Context, string, string, string, string) (domain.AuthorizationVerification, error)
}

type Handler struct {
	service Service
	banks   map[string]string
}

func New(service Service, certificateBanks map[string]string) *Handler {
	banks := make(map[string]string, len(certificateBanks))
	for fingerprint, bank := range certificateBanks {
		banks[fingerprint] = bank
	}
	return &Handler{service: service, banks: banks}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/authorization-verifications" {
		http.NotFound(w, r)
		return
	}
	bank, ok := h.bankForRequest(r)
	if !ok {
		writeProblem(w, http.StatusUnauthorized, "client authentication failed")
		return
	}
	var request struct {
		BankTransactionReference string `json:"bank_transaction_reference"`
		PAN                      string `json:"pan"`
		CVV                      string `json:"cvv"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) == nil || !valid(request) {
		writeProblem(w, http.StatusBadRequest, "invalid request")
		return
	}
	decision, err := h.service.Verify(r.Context(), bank, request.BankTransactionReference, request.PAN, request.CVV)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "verification unavailable")
		return
	}
	status := "declined"
	if decision.Approved {
		status = "approved"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"bank_transaction_reference": decision.BankTransactionRef,
		"decision_id":                decision.DecisionID,
		"decision":                   status,
	})
}

func (h *Handler) bankForRequest(r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		return "", false
	}
	sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	bank, found := h.banks[hex.EncodeToString(sum[:])]
	return bank, found
}

func valid(request struct {
	BankTransactionReference string `json:"bank_transaction_reference"`
	PAN                      string `json:"pan"`
	CVV                      string `json:"cvv"`
}) bool {
	return strings.TrimSpace(request.BankTransactionReference) != "" && len(request.BankTransactionReference) <= 256 &&
		len(request.PAN) >= 13 && len(request.PAN) <= 19 && digits(request.PAN) &&
		(len(request.CVV) == 3 || len(request.CVV) == 4) && digits(request.CVV)
}

func digits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": "authorization_verification_failed", "detail": detail})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
