package integration

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"card-issuer-api/internal/auth"
	"card-issuer-api/internal/database"
	"card-issuer-api/internal/server"
)

func TestRuntimeDatabaseEnvironment(t *testing.T) {
	if runtime.Version() != "go1.27.1" {
		t.Fatal("Go 1.27.1 is required")
	}
	if os.Getenv("CI_TEST_DATABASE") != "1" {
		t.Skip("set CI_TEST_DATABASE=1 against a disposable PostgreSQL 17 server")
	}
	psql := os.Getenv("PSQL")
	if psql == "" {
		psql = "psql"
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("ci_env_%d", time.Now().UnixNano())
	controlDB, shardDB := prefix+"_control", prefix+"_shard"
	sql := func(db, query string) (string, error) {
		cmd := exec.Command(psql, "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=sqlstate", "-d", db)
		cmd.Stdin = strings.NewReader(query)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	mustSQL := func(db, query string) string {
		t.Helper()
		out, err := sql(db, query)
		if err != nil {
			t.Fatalf("database check failed: %v %s", err, out)
		}
		return out
	}
	if version := mustSQL("postgres", "SHOW server_version_num;"); !strings.HasPrefix(version, "17") {
		t.Fatal("PostgreSQL 17 required")
	}
	t.Setenv("CONTROL_DATABASE", controlDB)
	t.Setenv("SHARD_DATABASE", shardDB)
	// Special characters verify environment-to-psql quoting and URL encoding.
	t.Setenv("CONTROL_DB_PASSWORD", "test-control ' $ & @ / : password")
	t.Setenv("AUTH_DB_PASSWORD", "test-auth ' $ & @ / : password")
	t.Setenv("SHARD_DB_PASSWORD", "test-shard ' $ & @ / : password")
	initialize := func() (string, error) {
		cmd := exec.Command("pwsh", "-NoProfile", "-File", filepath.Join(root, "database", "Initialize-Compose.ps1"), "-Psql", psql)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() {
		for _, db := range []string{controlDB, shardDB} {
			if !strings.HasPrefix(db, prefix+"_") {
				t.Error("unsafe cleanup target")
				continue
			}
			if out, err := sql("postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE);`, db)); err != nil {
				t.Errorf("cleanup failed: %v %s", err, out)
			}
		}
	})
	if out, err := initialize(); err != nil {
		t.Fatalf("initialization failed: %v %s", err, out)
	}
	if got := mustSQL(controlDB, "SELECT count(*) FROM control.users;"); got != "4" {
		t.Fatal("missing test accounts")
	}
	t.Run("repeated_initialization_preserves_staff_passwords", func(t *testing.T) {
		mustSQL(controlDB, "UPDATE control.users SET password_hash=password_hash||'changed',status='disabled' WHERE normalized_username='bank_operator';")
		before := mustSQL(controlDB, "SELECT md5(password_hash)||status||auth_version FROM control.users WHERE normalized_username='bank_operator';")
		if out, err := initialize(); err != nil {
			t.Fatalf("reinitialization failed: %v %s", err, out)
		}
		if after := mustSQL(controlDB, "SELECT md5(password_hash)||status||auth_version FROM control.users WHERE normalized_username='bank_operator';"); after != before {
			t.Fatal("seed reset a changed account")
		}
	})
	urlFor := func(user, password, db string) string {
		u := url.URL{Scheme: "postgres", Host: os.Getenv("PGHOST") + ":" + os.Getenv("PGPORT"), Path: "/" + db, User: url.UserPassword(user, password)}
		q := u.Query()
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
		return u.String()
	}
	ctx := context.Background()
	control, err := database.Open(ctx, urlFor("ci_app_control", os.Getenv("CONTROL_DB_PASSWORD"), controlDB))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	authPool, err := database.Open(ctx, urlFor("ci_app_auth", os.Getenv("AUTH_DB_PASSWORD"), controlDB))
	if err != nil {
		t.Fatal(err)
	}
	defer authPool.Close()
	shard, err := database.Open(ctx, urlFor("ci_app_shard", os.Getenv("SHARD_DB_PASSWORD"), shardDB))
	if err != nil {
		t.Fatal(err)
	}
	defer shard.Close()
	t.Run("runtime_logins_and_restrictions", func(t *testing.T) {
		for _, query := range []string{"SET ROLE ci_owner", "SELECT * FROM control.users", "SELECT * FROM ci_meta.schema_migrations", "CREATE ROLE forbidden_role"} {
			if _, err := control.Exec(ctx, query); err == nil {
				t.Fatalf("control runtime accepted forbidden statement: %s", query)
			}
		}
		var count int
		if err := control.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries").Scan(&count); err != nil || count != 1 {
			t.Fatal("routing reader cannot read directory")
		}
		if err := shard.QueryRow(ctx, "SELECT count(*) FROM bank.entities").Scan(&count); err != nil || count != 0 {
			t.Fatal("missing tenant did not fail closed")
		}
		for _, query := range []string{"SET ROLE ci_owner", "SET ROLE ci_auth_runtime", "ALTER ROLE ci_app_shard BYPASSRLS", "TRUNCATE bank.audit_events"} {
			if _, err := shard.Exec(ctx, query); err == nil {
				t.Fatalf("shard runtime accepted forbidden statement: %s", query)
			}
		}
		other, err := database.Open(ctx, urlFor("ci_app_shard", os.Getenv("SHARD_DB_PASSWORD"), controlDB))
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := other.Ping(checkCtx); err == nil {
			t.Fatal("shard login accessed the control database")
		}
	})
	t.Run("authentication_sessions_and_audits", func(t *testing.T) {
		signer, err := auth.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "integration", "card-issuer-api")
		if err != nil {
			t.Fatal(err)
		}
		service := auth.NewService(authPool, signer)
		login, root, _, err := service.Login(ctx, "issuer_operator", "Test-Issuer-Operator!2026", "90000000-0000-4000-8000-000000000001")
		if err != nil || login.User.Username != "issuer_operator" || root == "" {
			t.Fatalf("login failed: %v", err)
		}
		claims, err := service.ValidateAccess(login.AccessToken)
		if err != nil || claims.Username != "issuer_operator" {
			t.Fatalf("access token was invalid: %v", err)
		}
		first, rotated, _, err := service.Refresh(ctx, root, "90000000-0000-4000-8000-000000000002")
		if err != nil || first.AccessToken == "" || rotated == root {
			t.Fatalf("refresh failed: %v", err)
		}
		if _, _, _, err := service.Refresh(ctx, root, "90000000-0000-4000-8000-000000000003"); !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("refresh replay was accepted: %v", err)
		}
		var revoked bool
		if err := authPool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM control.auth_sessions WHERE id=$1", claims.SessionID).Scan(&revoked); err != nil || !revoked {
			t.Fatalf("replay did not revoke session: %v %v", revoked, err)
		}

		concurrent, concurrentRoot, _, err := service.Login(ctx, "issuer_operator", "Test-Issuer-Operator!2026", "90000000-0000-4000-8000-000000000004")
		if err != nil {
			t.Fatal(err)
		}
		concurrentClaims, err := service.ValidateAccess(concurrent.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		refreshResults := make(chan error, 2)
		for range 2 {
			go func() {
				_, _, _, refreshErr := service.Refresh(ctx, concurrentRoot, "90000000-0000-4000-8000-000000000005")
				refreshResults <- refreshErr
			}()
		}
		succeeded, rejected := 0, 0
		for range 2 {
			if refreshErr := <-refreshResults; refreshErr == nil {
				succeeded++
			} else if errors.Is(refreshErr, auth.ErrInvalidRefresh) {
				rejected++
			} else {
				t.Fatalf("unexpected concurrent refresh failure: %v", refreshErr)
			}
		}
		if succeeded != 1 || rejected != 1 {
			t.Fatalf("unexpected concurrent refresh results: %d succeeded, %d rejected", succeeded, rejected)
		}
		if err := authPool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM control.auth_sessions WHERE id=$1", concurrentClaims.SessionID).Scan(&revoked); err != nil || !revoked {
			t.Fatalf("concurrent replay did not revoke session: %v %v", revoked, err)
		}
		disabled, disabledToken, _, err := service.Login(ctx, "issuer_operator", "Test-Issuer-Operator!2026", "90000000-0000-4000-8000-000000000015")
		if err != nil {
			t.Fatal(err)
		}
		disabledClaims, err := service.ValidateAccess(disabled.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := authPool.Exec(ctx, "UPDATE control.users SET status='disabled' WHERE id=$1", disabledClaims.UserID); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := service.Refresh(ctx, disabledToken, "90000000-0000-4000-8000-000000000016"); !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("disabled user refreshed: %v", err)
		}
		if err := service.ChangePassword(ctx, disabledClaims, "Test-Issuer-Operator!2026", "ChangedIssuerPassword!2026", "90000000-0000-4000-8000-000000000017"); !errors.Is(err, auth.ErrInvalidPassword) {
			t.Fatalf("disabled user changed password: %v", err)
		}

		logout, logoutToken, _, err := service.Login(ctx, "issuer_readonly", "Test-Issuer-Readonly!2026", "90000000-0000-4000-8000-000000000006")
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Logout(ctx, logoutToken, "90000000-0000-4000-8000-000000000007"); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := service.Refresh(ctx, logoutToken, "90000000-0000-4000-8000-000000000008"); !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("logout did not revoke refresh: %v", err)
		}
		_ = logout

		passwordLogin, passwordToken, _, err := service.Login(ctx, "bank_readonly", "Test-Bank-Readonly!2026", "90000000-0000-4000-8000-000000000009")
		if err != nil {
			t.Fatal(err)
		}
		passwordClaims, err := service.ValidateAccess(passwordLogin.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.ChangePassword(ctx, passwordClaims, "Test-Bank-Readonly!2026", "ChangedPassword!2026", "90000000-0000-4000-8000-000000000010"); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := service.Refresh(ctx, passwordToken, "90000000-0000-4000-8000-000000000011"); !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("password change did not invalidate refresh family: %v", err)
		}
		if _, _, _, err := service.Login(ctx, "bank_readonly", "ChangedPassword!2026", "90000000-0000-4000-8000-000000000012"); err != nil {
			t.Fatalf("changed password could not log in: %v", err)
		}

		expired, expiringToken, _, err := service.Login(ctx, "issuer_readonly", "Test-Issuer-Readonly!2026", "90000000-0000-4000-8000-000000000013")
		if err != nil {
			t.Fatal(err)
		}
		expiringClaims, err := service.ValidateAccess(expired.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		// The schema correctly makes a session's absolute expiry immutable. This
		// disposable-database test temporarily bypasses that guard to create an
		// expired-session fixture without weakening the production invariant.
		mustSQL(controlDB, "ALTER TABLE control.auth_sessions DISABLE TRIGGER guard_session;")
		_, expireErr := authPool.Exec(ctx, "UPDATE control.auth_sessions SET created_at=now()-interval '7 days', last_refreshed_at=now()-interval '7 days', expires_at=now()-interval '1 second' WHERE id=$1", expiringClaims.SessionID)
		mustSQL(controlDB, "ALTER TABLE control.auth_sessions ENABLE TRIGGER guard_session;")
		if expireErr != nil {
			t.Fatal(expireErr)
		}
		if _, _, _, err := service.Refresh(ctx, expiringToken, "90000000-0000-4000-8000-000000000014"); !errors.Is(err, auth.ErrInvalidRefresh) {
			t.Fatalf("expired session refreshed: %v", err)
		}

		var details string
		if err := authPool.QueryRow(ctx, "SELECT COALESCE(string_agg(details::text,' '),'') FROM control.authentication_audit_events WHERE request_id BETWEEN '90000000-0000-4000-8000-000000000001' AND '90000000-0000-4000-8000-000000000017'").Scan(&details); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(details, root) || strings.Contains(details, rotated) || strings.Contains(details, passwordToken) {
			t.Fatal("authentication audit exposed a refresh token")
		}
	})
	t.Run("live_readiness_outage_recovery", func(t *testing.T) {
		handler := server.Handler(control.Ping, control.Ping, shard.Ping, nil)
		check := func(path string, want int) {
			t.Helper()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != want {
				t.Fatalf("%s returned %d; want %d", path, w.Code, want)
			}
		}
		check("/health/ready", 200)
		// Disable only this disposable database and disconnect its pool to simulate unavailability.
		mustSQL("postgres", fmt.Sprintf(`ALTER DATABASE %q ALLOW_CONNECTIONS false;`, shardDB))
		defer mustSQL("postgres", fmt.Sprintf(`ALTER DATABASE %q ALLOW_CONNECTIONS true;`, shardDB))
		mustSQL("postgres", fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='%s';", shardDB))
		shard.Reset()
		check("/health/live", 200)
		check("/health/ready", 503)
		mustSQL("postgres", fmt.Sprintf(`ALTER DATABASE %q ALLOW_CONNECTIONS true;`, shardDB))
		check("/health/ready", 200)
	})
	t.Run("existing_technical_password_is_not_reset", func(t *testing.T) {
		// Requires SCRAM/password authentication on the disposable server to be meaningful.
		t.Setenv("CONTROL_DB_PASSWORD", "intentionally-wrong-test-password")
		if _, err := initialize(); err == nil {
			t.Fatal("wrong technical password was accepted; ensure this test server requires password authentication")
		}
	})
	t.Run("incompatible_runtime_account_is_rejected", func(t *testing.T) {
		mustSQL("postgres", "ALTER ROLE ci_app_control BYPASSRLS;")
		defer mustSQL("postgres", "ALTER ROLE ci_app_control NOBYPASSRLS;")
		if out, err := initialize(); err == nil || !strings.Contains(out, "incompatible") {
			t.Fatal("incompatible runtime privileges were accepted")
		}
	})
}
