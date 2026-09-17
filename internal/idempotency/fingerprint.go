// Package idempotency contains application-neutral request replay primitives.
package idempotency

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

const minimumKeyLength = 32

// Fingerprinter creates keyed request fingerprints for idempotent commands.
// Its key is immutable after construction, so a Fingerprinter may be shared by
// concurrent callers.
type Fingerprinter struct {
	key string
}

// NewFingerprinter constructs a Fingerprinter from a key with at least 256 bits
// of key material. Converting the key to a string creates an immutable copy.
func NewFingerprinter(key []byte) (*Fingerprinter, error) {
	if len(key) < minimumKeyLength {
		return nil, errors.New("idempotency HMAC key must be at least 32 bytes")
	}
	return &Fingerprinter{key: string(key)}, nil
}

// Fingerprint returns the stable HMAC-SHA-256 fingerprint used to compare the
// normalized form of an idempotent command request.
func (f *Fingerprinter) Fingerprint(payload []byte) []byte {
	mac := hmac.New(sha256.New, []byte(f.key))
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}
