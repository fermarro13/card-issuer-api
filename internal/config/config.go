package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
)

const publicDevelopmentIdempotencyKeyB64 = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE"

type Config struct {
	Environment         string
	VaultMode           string
	DevelopmentVaultURL string
	HTTPAddress         string
	AuthURL             string
	ControlURL          string
	ShardURL            string
	ShardID             string
	CursorKey           []byte
	IdempotencyKey      []byte
	JWTPrivateKey       ed25519.PrivateKey
	JWTIssuer           string
	JWTAudience         string
}

func FromEnvironment() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	value := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	address := value("HTTP_ADDR", ":8080")
	environment := value("APP_ENV", "development")
	if environment != "development" && environment != "test" && environment != "production" {
		return Config{}, errors.New("invalid APP_ENV")
	}
	vaultMode := value("CREDENTIAL_VAULT_MODE", "external")
	if vaultMode != "development" && vaultMode != "external" {
		return Config{}, errors.New("invalid CREDENTIAL_VAULT_MODE")
	}
	if vaultMode == "development" && environment != "development" && environment != "test" {
		return Config{}, errors.New("CREDENTIAL_VAULT_MODE=development is not allowed outside development or test")
	}
	developmentVaultURL := ""
	if vaultMode == "development" {
		developmentVaultURL = getenv("DEVELOPMENT_VAULT_URL")
		developmentURL, parseErr := url.Parse(developmentVaultURL)
		if parseErr != nil || developmentURL.Scheme != "http" || developmentURL.Host == "" || developmentURL.User != nil || developmentURL.RawQuery != "" || developmentURL.Fragment != "" || (developmentURL.Path != "" && developmentURL.Path != "/") {
			return Config{}, errors.New("invalid DEVELOPMENT_VAULT_URL")
		}
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return Config{}, errors.New("invalid HTTP_ADDR")
	}
	port := value("PGPORT", "5432")
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return Config{}, errors.New("invalid PGPORT")
	}
	sslmode := value("PGSSLMODE", "disable")
	if sslmode != "disable" && sslmode != "require" && sslmode != "verify-ca" && sslmode != "verify-full" {
		return Config{}, errors.New("invalid PGSSLMODE")
	}
	connectionURL := func(prefix, defaultDatabase, defaultUser string) (string, error) {
		password := getenv(prefix + "_DB_PASSWORD")
		if password == "" {
			return "", errors.New(prefix + "_DB_PASSWORD is required")
		}
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(value("PGHOST", "postgres"), port), Path: "/" + value(prefix+"_DATABASE", defaultDatabase), User: url.UserPassword(value(prefix+"_DB_USER", defaultUser), password)}
		q := u.Query()
		q.Set("sslmode", sslmode)
		q.Set("connect_timeout", "2")
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	auth, err := connectionURL("AUTH", value("CONTROL_DATABASE", "card_issuer_control"), "ci_app_auth")
	if err != nil {
		return Config{}, err
	}
	control, err := connectionURL("CONTROL", "card_issuer_control", "ci_app_control")
	if err != nil {
		return Config{}, err
	}
	shard, err := connectionURL("SHARD", "card_issuer_shard_01", "ci_app_shard")
	if err != nil {
		return Config{}, err
	}
	shardID := value("SHARD_ID", "shard_01")
	if shardID == "" || len(shardID) > 63 {
		return Config{}, errors.New("invalid SHARD_ID")
	}
	cursorKey, err := base64.RawStdEncoding.DecodeString(getenv("API_CURSOR_HMAC_KEY_B64"))
	if err != nil {
		cursorKey, err = base64.StdEncoding.DecodeString(getenv("API_CURSOR_HMAC_KEY_B64"))
	}
	if err != nil || len(cursorKey) < 32 {
		return Config{}, errors.New("invalid API_CURSOR_HMAC_KEY_B64")
	}
	idempotencyKey, err := base64.RawStdEncoding.DecodeString(getenv("API_IDEMPOTENCY_HMAC_KEY_B64"))
	if err != nil {
		idempotencyKey, err = base64.StdEncoding.DecodeString(getenv("API_IDEMPOTENCY_HMAC_KEY_B64"))
	}
	if err != nil || len(idempotencyKey) < 32 {
		return Config{}, errors.New("invalid API_IDEMPOTENCY_HMAC_KEY_B64")
	}
	if bytes.Equal(idempotencyKey, cursorKey) {
		return Config{}, errors.New("API_IDEMPOTENCY_HMAC_KEY_B64 must differ from API_CURSOR_HMAC_KEY_B64")
	}
	if environment == "production" && base64.RawStdEncoding.EncodeToString(idempotencyKey) == publicDevelopmentIdempotencyKeyB64 {
		return Config{}, errors.New("public development API_IDEMPOTENCY_HMAC_KEY_B64 is not allowed in production")
	}
	privateKey, err := base64.RawStdEncoding.DecodeString(getenv("AUTH_JWT_PRIVATE_KEY_B64"))
	if err != nil {
		privateKey, err = base64.StdEncoding.DecodeString(getenv("AUTH_JWT_PRIVATE_KEY_B64"))
	}
	if err != nil || len(privateKey) != ed25519.PrivateKeySize || !slices.Equal(privateKey, ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])) {
		return Config{}, errors.New("invalid AUTH_JWT_PRIVATE_KEY_B64")
	}
	issuer := getenv("AUTH_JWT_ISSUER")
	audience := getenv("AUTH_JWT_AUDIENCE")
	if issuer == "" || audience == "" {
		return Config{}, errors.New("AUTH_JWT_ISSUER and AUTH_JWT_AUDIENCE are required")
	}
	return Config{Environment: environment, VaultMode: vaultMode, DevelopmentVaultURL: developmentVaultURL, HTTPAddress: address, AuthURL: auth, ControlURL: control, ShardURL: shard, ShardID: shardID, CursorKey: cursorKey, IdempotencyKey: idempotencyKey, JWTPrivateKey: ed25519.PrivateKey(privateKey), JWTIssuer: issuer, JWTAudience: audience}, nil
}

// DevVaultConfig is the minimal configuration needed by cmd/devvault.
type DevVaultConfig struct {
	Environment  string
	Address      string
	TestVaultKey []byte
}

// DevelopmentVaultFromEnvironment loads only development-vault settings. The
// standalone process intentionally has no database configuration.
func DevelopmentVaultFromEnvironment() (DevVaultConfig, error) {
	value := func(name, fallback string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return fallback
	}
	environment := os.Getenv("APP_ENV")
	if environment != "development" && environment != "test" {
		return DevVaultConfig{}, errors.New("development vault is allowed only in development or test")
	}
	address := value("DEV_VAULT_ADDR", ":8081")
	if _, _, err := net.SplitHostPort(address); err != nil {
		return DevVaultConfig{}, errors.New("invalid DEV_VAULT_ADDR")
	}
	key, err := base64.RawStdEncoding.DecodeString(os.Getenv("TEST_VAULT_KEY_B64"))
	if err != nil {
		key, err = base64.StdEncoding.DecodeString(os.Getenv("TEST_VAULT_KEY_B64"))
	}
	if err != nil || len(key) < 32 {
		return DevVaultConfig{}, errors.New("invalid TEST_VAULT_KEY_B64")
	}
	return DevVaultConfig{Environment: environment, Address: address, TestVaultKey: key}, nil
}
