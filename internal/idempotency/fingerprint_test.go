package idempotency

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"sync"
	"testing"
)

func TestFingerprinterIsDeterministicAndSensitive(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	otherKey := bytes.Repeat([]byte{0x22}, 32)
	fingerprinter, err := NewFingerprinter(key)
	if err != nil {
		t.Fatal(err)
	}
	otherFingerprinter, err := NewFingerprinter(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"name":"bank"}`)
	first := fingerprinter.Fingerprint(payload)
	second := fingerprinter.Fingerprint(payload)
	differentPayload := fingerprinter.Fingerprint([]byte(`{"name":"other"}`))
	differentKey := otherFingerprinter.Fingerprint(payload)

	if !bytes.Equal(first, second) {
		t.Fatal("same payload produced different fingerprints")
	}
	if len(first) != sha256.Size {
		t.Fatalf("fingerprint length = %d, want %d", len(first), sha256.Size)
	}
	if bytes.Equal(first, differentPayload) {
		t.Fatal("different payloads produced the same fingerprint")
	}
	if bytes.Equal(first, differentKey) {
		t.Fatal("different keys produced the same fingerprint")
	}
}

func TestNewFingerprinterDefensivelyCopiesKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x33}, 32)
	original := bytes.Clone(key)
	fingerprinter, err := NewFingerprinter(key)
	if err != nil {
		t.Fatal(err)
	}
	for i := range key {
		key[i] = 0xff
	}
	payload := []byte("payload")
	mac := hmac.New(sha256.New, original)
	_, _ = mac.Write(payload)
	if got, want := fingerprinter.Fingerprint(payload), mac.Sum(nil); !bytes.Equal(got, want) {
		t.Fatal("fingerprinter key changed after the constructor input was mutated")
	}
}

func TestFingerprinterSupportsConcurrentUse(t *testing.T) {
	fingerprinter, err := NewFingerprinter(bytes.Repeat([]byte{0x34}, 32))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("shared payload")
	want := fingerprinter.Fingerprint(payload)
	results := make(chan []byte, 32)
	var callers sync.WaitGroup
	for range 32 {
		callers.Go(func() {
			results <- fingerprinter.Fingerprint(payload)
		})
	}
	callers.Wait()
	close(results)
	for got := range results {
		if !bytes.Equal(got, want) {
			t.Fatal("concurrent caller received a different fingerprint")
		}
	}
}

func TestNewFingerprinterRejectsShortKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  []byte
	}{
		{name: "empty", key: nil},
		{name: "31 bytes", key: make([]byte, 31)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewFingerprinter(tc.key); err == nil {
				t.Fatal("short key was accepted")
			}
		})
	}
}

func TestFingerprinterOutputDiffersFromPlainSHA256(t *testing.T) {
	fingerprinter, err := NewFingerprinter(bytes.Repeat([]byte{0x44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"password":"Correct-Horse-9!"}`)
	plain := sha256.Sum256(payload)
	if bytes.Equal(fingerprinter.Fingerprint(payload), plain[:]) {
		t.Fatal("HMAC fingerprint matched plain SHA-256")
	}
}
