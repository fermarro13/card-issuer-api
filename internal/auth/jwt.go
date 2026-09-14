package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const accessLifetime = 15 * time.Minute

type Signer struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	issuer     string
	audience   string
	now        func() time.Time
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

type jwtClaims struct {
	Subject   string `json:"sub"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	EntityID  string `json:"entity_id,omitempty"`
	SessionID string `json:"sid"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	JWTID     string `json:"jti"`
}

func NewSigner(privateKey ed25519.PrivateKey, issuer, audience string) (*Signer, error) {
	if len(privateKey) != ed25519.PrivateKeySize || !bytes.Equal(privateKey, ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])) || issuer == "" || audience == "" {
		return nil, errors.New("invalid JWT signing configuration")
	}
	return &Signer{privateKey: privateKey, publicKey: privateKey.Public().(ed25519.PublicKey), issuer: issuer, audience: audience, now: time.Now}, nil
}

func (s *Signer) Issue(user UserRecord, sessionID string) (TokenResponse, error) {
	issued := s.now().UTC()
	jwtID, err := newUUID()
	if err != nil {
		return TokenResponse{}, err
	}
	claims := jwtClaims{Subject: user.ID, Username: user.Username, Role: user.Role, Status: user.Status, EntityID: user.EntityID, SessionID: sessionID, Issuer: s.issuer, Audience: s.audience, IssuedAt: issued.Unix(), ExpiresAt: issued.Add(accessLifetime).Unix(), JWTID: jwtID}
	payload, err := json.Marshal(claims)
	if err != nil {
		return TokenResponse{}, err
	}
	header, _ := json.Marshal(jwtHeader{Algorithm: "EdDSA", Type: "JWT"})
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := encodedHeader + "." + encodedPayload
	signature := ed25519.Sign(s.privateKey, []byte(signingInput))
	return TokenResponse{AccessToken: signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), TokenType: "Bearer", ExpiresAt: issued.Add(accessLifetime).Format(time.RFC3339), User: user.summary()}, nil
}

func (s *Signer) Validate(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, ErrInvalidAccess
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidAccess
	}
	var header jwtHeader
	if err := decodeStrictJSON(headerJSON, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" {
		return Claims{}, ErrInvalidAccess
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(s.publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return Claims{}, ErrInvalidAccess
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidAccess
	}
	var claims jwtClaims
	if err := decodeStrictJSON(payload, &claims); err != nil || !validClaims(claims, s.issuer, s.audience, s.now().UTC()) {
		return Claims{}, ErrInvalidAccess
	}
	return Claims{UserSummary: UserSummary{Username: claims.Username, Status: claims.Status, Role: claims.Role}, UserID: claims.Subject, SessionID: claims.SessionID, EntityID: claims.EntityID}, nil
}

func validClaims(claims jwtClaims, issuer, audience string, now time.Time) bool {
	if !validUUID(claims.Subject) || claims.Username == "" || !validUUID(claims.SessionID) || !validUUID(claims.JWTID) || claims.Issuer != issuer || claims.Audience != audience || claims.IssuedAt <= 0 || claims.ExpiresAt <= claims.IssuedAt || now.Unix() >= claims.ExpiresAt || claims.Status != "enabled" || !validRole(claims.Role) {
		return false
	}
	bankRole := claims.Role == "bank_operator" || claims.Role == "bank_readonly"
	return bankRole == (claims.EntityID != "") && (!bankRole || validUUID(claims.EntityID))
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected JSON value")
	}
	return nil
}

func validRole(role string) bool {
	return role == "issuer_operator" || role == "issuer_readonly" || role == "bank_operator" || role == "bank_readonly"
}

func newUUID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate random UUID: %w", err)
	}
	bytes[6] = bytes[6]&0x0f | 0x40
	bytes[8] = bytes[8]&0x3f | 0x80
	return formatUUID(bytes), nil
}

func validUUID(value string) bool {
	if len(value) != 36 || strings.ToLower(value) != value {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func formatUUID(bytes []byte) string {
	encoded := make([]byte, 36)
	const hex = "0123456789abcdef"
	position := 0
	for index, value := range bytes {
		if index == 4 || index == 6 || index == 8 || index == 10 {
			encoded[position] = '-'
			position++
		}
		encoded[position] = hex[value>>4]
		encoded[position+1] = hex[value&0x0f]
		position += 2
	}
	return string(encoded)
}
