package idempotency

import (
	"bytes"
	"testing"
)

func TestFingerprintIsStableAndContentSensitive(t *testing.T) {
	first := Fingerprint([]byte(`{"name":"bank"}`))
	second := Fingerprint([]byte(`{"name":"bank"}`))
	different := Fingerprint([]byte(`{"name":"other"}`))

	if !bytes.Equal(first, second) {
		t.Fatal("same payload produced different fingerprints")
	}
	if bytes.Equal(first, different) {
		t.Fatal("different payloads produced the same fingerprint")
	}
}
