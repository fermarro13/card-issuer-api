package config

import (
	"net/url"
	"strings"
	"testing"
)

func TestDatabaseCredentialsAreEncoded(t *testing.T) {
	values := map[string]string{"CONTROL_DB_PASSWORD": "quote' @:/?&=\\$ control", "SHARD_DB_PASSWORD": "shard password", "CONTROL_DATABASE": "control db"}
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
	if strings.Contains(cfg.ShardURL, "ci_app_control") {
		t.Fatal("database identities were mixed")
	}
}

func TestInvalidConfigurationDoesNotExposeCredentials(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{"missing password", map[string]string{}},
		{"invalid port", map[string]string{"PGPORT": "secret@invalid"}},
		{"invalid listen", map[string]string{"HTTP_ADDR": "secret@invalid"}},
		{"invalid sslmode", map[string]string{"PGSSLMODE": "secret@invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(func(key string) string { return tc.values[key] })
			if err == nil || strings.Contains(err.Error(), "secret@invalid") {
				t.Fatalf("expected a sanitized configuration error")
			}
		})
	}
}
