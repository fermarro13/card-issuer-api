package vault

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
)

var ErrNotAllowed = errors.New("vault: in-memory adapter is restricted to development and test")

// InMemory is deliberately test-only. It retains keyed lookup tokens and card
// identities, never PAN or CVV values. A protected test key derives the
// transient values when needed.
type InMemory struct {
	key     []byte
	mu      sync.RWMutex
	cards   map[string]string
	revoked map[string]bool
}

func NewInMemory(environment string, key []byte) (*InMemory, error) {
	if environment != "development" && environment != "test" {
		return nil, ErrNotAllowed
	}
	if len(key) < 32 {
		return nil, errors.New("vault: test key must contain at least 32 bytes")
	}
	return &InMemory{
		key:     append([]byte(nil), key...),
		cards:   map[string]string{},
		revoked: map[string]bool{},
	}, nil
}

func (v *InMemory) Provision(_ context.Context, request ProvisionRequest) (ProvisionedCredential, error) {
	if request.CardID == "" {
		return ProvisionedCredential{}, errors.New("vault: card id is required")
	}
	pan := v.pan(request.CardID)
	v.mu.Lock()
	v.cards[v.lookupToken(pan)] = request.CardID
	delete(v.revoked, request.CardID)
	v.mu.Unlock()
	return ProvisionedCredential{MaskedPAN: maskPAN(pan), PAN: pan, CVV: v.cvv(request.CardID)}, nil
}

func (v *InMemory) Verify(_ context.Context, request VerificationRequest) (VerificationResult, error) {
	if request.PAN == "" || request.CVV == "" {
		return VerificationResult{Valid: false}, nil
	}
	v.mu.RLock()
	cardID, found := v.cards[v.lookupToken(request.PAN)]
	isRevoked := v.revoked[cardID]
	v.mu.RUnlock()
	if !found || isRevoked || !hmac.Equal([]byte(v.cvv(cardID)), []byte(request.CVV)) {
		return VerificationResult{Valid: false}, nil
	}
	return VerificationResult{CardID: cardID, Valid: true}, nil
}

func (v *InMemory) Revoke(_ context.Context, cardID string) error {
	if cardID == "" {
		return errors.New("vault: card id is required")
	}
	v.mu.Lock()
	v.revoked[cardID] = true
	v.mu.Unlock()
	return nil
}

func (v *InMemory) pan(cardID string) string {
	sum := v.derive("pan:" + cardID)
	return fmt.Sprintf("400000%010d", decimal(sum[:8])%10_000_000_000)
}

func (v *InMemory) cvv(cardID string) string {
	return fmt.Sprintf("%03d", decimal(v.derive("cvv:" + cardID)[:4])%1_000)
}

func (v *InMemory) lookupToken(pan string) string {
	return hex.EncodeToString(v.derive("lookup:" + pan))
}

func (v *InMemory) derive(value string) []byte {
	mac := hmac.New(sha256.New, v.key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func decimal(value []byte) uint64 {
	var result uint64
	for _, b := range value {
		result = result<<8 | uint64(b)
	}
	return result
}

func maskPAN(pan string) string {
	if len(pan) < 10 {
		return ""
	}
	return pan[:6] + "******" + pan[len(pan)-4:]
}
