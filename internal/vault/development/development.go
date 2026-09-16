// Package development provides the development-only shared credential vault transport.
package development

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"card-issuer-api/internal/vault"
)

const maxBodyBytes = 2048

var errUnavailable = errors.New("vault: development vault unavailable")

// Handler exposes the narrow development-only vault protocol. It must only be
// bound to a trusted internal network.
type Handler struct {
	vault vault.CredentialVault
}

// NewHandler wraps a credential vault for the development transport.
func NewHandler(credentialVault vault.CredentialVault) *Handler {
	return &Handler{vault: credentialVault}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound)
		return
	}

	switch r.URL.Path {
	case "/v1/provision":
		var request struct {
			CardID string `json:"card_id"`
		}
		if !decode(r, w, &request) || strings.TrimSpace(request.CardID) == "" {
			writeError(w, http.StatusBadRequest)
			return
		}
		disclosure, err := h.vault.Provision(r.Context(), vault.ProvisionRequest{CardID: request.CardID})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, disclosure)
	case "/v1/verify":
		var request struct {
			PAN string `json:"pan"`
			CVV string `json:"cvv"`
		}
		if !decode(r, w, &request) || !digits(request.PAN) || !digits(request.CVV) {
			writeError(w, http.StatusBadRequest)
			return
		}
		result, err := h.vault.Verify(r.Context(), vault.VerificationRequest{PAN: request.PAN, CVV: request.CVV})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case "/v1/revoke":
		var request struct {
			CardID string `json:"card_id"`
		}
		if !decode(r, w, &request) || strings.TrimSpace(request.CardID) == "" {
			writeError(w, http.StatusBadRequest)
			return
		}
		if err := h.vault.Revoke(r.Context(), request.CardID); err != nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusNotFound)
	}
}

func decode(r *http.Request, w http.ResponseWriter, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func digits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int) {
	writeJSON(w, status, map[string]string{"error": "vault request failed"})
}

// Client implements vault.CredentialVault over the development-only protocol.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient validates the internal endpoint and configures bounded requests.
func NewClient(rawURL string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("vault: invalid development vault URL")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 2 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{baseURL: strings.TrimRight(u.String(), "/"), http: &http.Client{Transport: transport, Timeout: 5 * time.Second}}, nil
}

// Provision requests an initial one-time credential disclosure.
func (c *Client) Provision(ctx context.Context, request vault.ProvisionRequest) (vault.ProvisionedCredential, error) {
	var response struct {
		MaskedPAN *string `json:"masked_pan"`
		PAN       *string `json:"pan"`
		CVV       *string `json:"cvv"`
	}
	if err := c.post(ctx, "/v1/provision", request, &response); err != nil {
		return vault.ProvisionedCredential{}, err
	}
	if response.MaskedPAN == nil || response.PAN == nil || response.CVV == nil {
		return vault.ProvisionedCredential{}, errUnavailable
	}
	result := vault.ProvisionedCredential{MaskedPAN: *response.MaskedPAN, PAN: *response.PAN, CVV: *response.CVV}
	if result.MaskedPAN == "" || len(result.PAN) < 13 || len(result.PAN) > 19 || !digits(result.PAN) || (len(result.CVV) != 3 && len(result.CVV) != 4) || !digits(result.CVV) {
		return vault.ProvisionedCredential{}, errUnavailable
	}
	return result, nil
}

// Verify checks credentials without persisting them in the caller process.
func (c *Client) Verify(ctx context.Context, request vault.VerificationRequest) (vault.VerificationResult, error) {
	var response struct {
		CardID *string `json:"card_id"`
		Valid  *bool   `json:"valid"`
	}
	if err := c.post(ctx, "/v1/verify", request, &response); err != nil {
		return vault.VerificationResult{}, err
	}
	if response.CardID == nil || response.Valid == nil {
		return vault.VerificationResult{}, errUnavailable
	}
	result := vault.VerificationResult{CardID: *response.CardID, Valid: *response.Valid}
	if (result.Valid && strings.TrimSpace(result.CardID) == "") || (!result.Valid && result.CardID != "") {
		return vault.VerificationResult{}, errUnavailable
	}
	return result, nil
}

// Revoke prevents later verification of a card's development credentials.
func (c *Client) Revoke(ctx context.Context, cardID string) error {
	return c.post(ctx, "/v1/revoke", struct {
		CardID string `json:"card_id"`
	}{CardID: cardID}, nil)
}

func (c *Client) post(ctx context.Context, path string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return errUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return errUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return errUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return errUnavailable
	}
	if output == nil {
		return nil
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return errUnavailable
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errUnavailable
	}
	return nil
}
