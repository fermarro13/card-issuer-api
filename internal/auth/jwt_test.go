package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestJWTValidationRejectsInvalidSecurityProperties(t *testing.T) {
	signer, err := NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "issuer", "audience")
	if err != nil {
		t.Fatal(err)
	}
	signer.now = func() time.Time { return time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC) }
	response, err := signer.Issue(UserRecord{ID: "20000000-0000-4000-8000-000000000001", Username: "operator", Role: "issuer_operator", Status: "enabled"}, "30000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := signer.Validate(response.AccessToken)
	if err != nil || claims.Username != "operator" || claims.Role != "issuer_operator" {
		t.Fatalf("valid token rejected: %#v %v", claims, err)
	}
	parts := strings.Split(response.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatal("invalid issued token")
	}
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	wrongAlgorithm := base64.RawURLEncoding.EncodeToString(header) + "." + parts[1] + "." + parts[2]
	if _, err := signer.Validate(wrongAlgorithm); err != ErrInvalidAccess {
		t.Fatalf("wrong algorithm accepted: %v", err)
	}
	wrongIssuer, _ := NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "other", "audience")
	if _, err := wrongIssuer.Validate(response.AccessToken); err != ErrInvalidAccess {
		t.Fatalf("wrong issuer accepted: %v", err)
	}
	wrongAudience, _ := NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "issuer", "other")
	if _, err := wrongAudience.Validate(response.AccessToken); err != ErrInvalidAccess {
		t.Fatalf("wrong audience accepted: %v", err)
	}
	signer.now = func() time.Time { return time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC) }
	if _, err := signer.Validate(response.AccessToken); err != ErrInvalidAccess {
		t.Fatalf("expired token accepted: %v", err)
	}
}
