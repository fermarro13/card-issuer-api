// Package idempotency contains application-neutral request replay primitives.
package idempotency

import "crypto/sha256"

// Fingerprint returns the stable SHA-256 fingerprint used to compare the
// normalized form of an idempotent command request.
func Fingerprint(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return sum[:]
}
