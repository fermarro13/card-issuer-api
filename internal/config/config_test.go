package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestDatabaseCredentialsAreEncoded(t *testing.T) {
	values := validValues()
	values["CONTROL_DB_PASSWORD"] = "quote' @:/?&=\\$ control"
	values["AUTH_DB_PASSWORD"] = "auth password"
	values["SHARD_DB_PASSWORD"] = "shard password"
	values["CONTROL_DATABASE"] = "control db"
	cfg, err := load(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(cfg.ControlURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if password != values["CONTROL_DB_PASSWORD"] || u.Path != "/control db" || u.User.Username() != "ci_app_control" {
		t.Fatal("connection values were not preserved")
	}
	if strings.Contains(cfg.ShardURL, "ci_app_control") || !strings.Contains(cfg.AuthURL, "ci_app_auth") {
		t.Fatal("database identities were mixed")
	}
}

func TestInvalidConfigurationDoesNotExposeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"missing password", map[string]string{}},
		{"invalid port", with("PGPORT", "secret@invalid")},
		{"invalid listen", with("HTTP_ADDR", "secret@invalid")},
		{"invalid sslmode", with("PGSSLMODE", "secret@invalid")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(func(key string) string { return tc.values[key] })
			if err == nil || strings.Contains(err.Error(), "secret@invalid") {
				t.Fatalf("expected a sanitized configuration error")
			}
		})
	}
}

func TestDevelopmentVaultIsRejectedOutsideDevelopmentAndTest(t *testing.T) {
	values := validValues()
	values["APP_ENV"] = "production"
	values["CREDENTIAL_VAULT_MODE"] = "development"
	values["DEVELOPMENT_VAULT_URL"] = "http://devvault:8081"
	_, err := load(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("production development vault error = %v", err)
	}
}

func TestDevelopmentVaultRequiresInternalURL(t *testing.T) {
	values := validValues()
	values["CREDENTIAL_VAULT_MODE"] = "development"
	values["DEVELOPMENT_VAULT_URL"] = "https://devvault:8081"
	_, err := load(func(key string) string { return values[key] })
	if err == nil || !strings.Contains(err.Error(), "DEVELOPMENT_VAULT_URL") {
		t.Fatalf("development URL error = %v", err)
	}
}

func TestExternalVaultDoesNotLoadDevelopmentVault(t *testing.T) {
	values := validValues()
	values["CREDENTIAL_VAULT_MODE"] = "external"
	values["DEVELOPMENT_VAULT_URL"] = "not a URL"
	cfg, err := load(func(key string) string { return values[key] })
	if err != nil || cfg.DevelopmentVaultURL != "" {
		t.Fatalf("external vault configuration = %#v, %v", cfg, err)
	}
}

func TestDevelopmentVaultProcessIsLimitedToDevelopmentAndTest(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DEV_VAULT_ADDR", "127.0.0.1:8081")
	t.Setenv("TEST_VAULT_KEY_B64", base64.RawStdEncoding.EncodeToString(make([]byte, 32)))
	cfg, err := DevelopmentVaultFromEnvironment()
	if err != nil || cfg.Address != "127.0.0.1:8081" {
		t.Fatalf("development vault configuration = %#v, %v", cfg, err)
	}
	t.Setenv("APP_ENV", "production")
	if _, err := DevelopmentVaultFromEnvironment(); err == nil {
		t.Fatal("production development vault process was accepted")
	}
}

func validValues() map[string]string {
	return map[string]string{
		"CONTROL_DB_PASSWORD": "control", "AUTH_DB_PASSWORD": "auth", "SHARD_DB_PASSWORD": "shard",
		"AUTH_JWT_PRIVATE_KEY_B64": base64.RawStdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))),
		"AUTH_JWT_ISSUER":          "issuer", "AUTH_JWT_AUDIENCE": "audience",
		"API_CURSOR_HMAC_KEY_B64": base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
		"TEST_VAULT_KEY_B64":      base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
	}
}

func with(key, value string) map[string]string {
	values := validValues()
	values[key] = value
	return values
}
