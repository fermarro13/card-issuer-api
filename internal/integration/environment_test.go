package integration

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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
	authrepository "card-issuer-api/internal/repository/auth"
	controlrepository "card-issuer-api/internal/repository/control"
	routingrepository "card-issuer-api/internal/repository/routing"
	shardrepository "card-issuer-api/internal/repository/shard"
	"card-issuer-api/internal/resource"
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
	sql := func(db, query string, variables ...string) (string, error) {
		args := []string{"-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=sqlstate", "-d", db}
		for _, variable := range variables {
			args = append(args, "-v", variable)
		}
		cmd := exec.Command(psql, args...)
		cmd.Stdin = strings.NewReader(query)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	mustSQL := func(db, query string, variables ...string) string {
		t.Helper()
		out, err := sql(db, query, variables...)
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
		service := auth.NewService(authrepository.New(authPool), signer)
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
	t.Run("public_resource_http_contract", func(t *testing.T) {
		// The authentication scenario above disables this fixture intentionally.
		// Re-enable it here to test the public API through the restricted runtime pools.
		mustSQL(controlDB, "UPDATE control.users SET status='enabled' WHERE normalized_username='issuer_operator';")
		signer, err := auth.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), "integration", "card-issuer-api")
		if err != nil {
			t.Fatal(err)
		}
		service := auth.NewService(authrepository.New(authPool), signer)
		login, _, _, err := service.Login(ctx, "issuer_operator", "Test-Issuer-Operator!2026", "90000000-0000-4000-8000-000000000101")
		if err != nil {
			t.Fatal(err)
		}
		business := resource.New(service, authPool, controlrepository.New(authPool), routingrepository.New(control), shardrepository.NewReader(shard), shard, "shard_01", make([]byte, 32))
		handler := server.Handler(authPool.Ping, control.Ping, shard.Ping, service, business)
		request := func(method, path, body, key string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+login.AccessToken)
			if body != "" {
				r.Header.Set("Content-Type", "application/json")
			}
			if key != "" {
				r.Header.Set("Idempotency-Key", key)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return w
		}
		provisionBody := `{"bank_reference":"RESOURCE_PROVISION_TEST","name":"Resource Provision Test"}`
		provisioned := request(http.MethodPost, "/v1/banks", provisionBody, "resource-provision")
		if provisioned.Code != http.StatusCreated {
			t.Fatalf("bank provision: %d %s", provisioned.Code, provisioned.Body.String())
		}
		var provisionedBank map[string]any
		if err := json.Unmarshal(provisioned.Body.Bytes(), &provisionedBank); err != nil {
			t.Fatal(err)
		}
		provisionedID, _ := provisionedBank["id"].(string)
		if provisionedID == "" {
			t.Fatalf("provision response missing bank id: %s", provisioned.Body.String())
		}
		replayedProvision := request(http.MethodPost, "/v1/banks", provisionBody, "resource-provision")
		if replayedProvision.Code != http.StatusCreated || replayedProvision.Body.String() != provisioned.Body.String() {
			t.Fatalf("bank provision replay: %d %s", replayedProvision.Code, replayedProvision.Body.String())
		}
		provisionedGet := request(http.MethodGet, "/v1/banks/"+provisionedID, "", "")
		if provisionedGet.Code != http.StatusOK {
			t.Fatalf("provisioned bank read: %d %s", provisionedGet.Code, provisionedGet.Body.String())
		}
		bank := request(http.MethodGet, "/v1/banks/10000000-0000-4000-8000-000000000001", "", "")
		if bank.Code != http.StatusOK {
			t.Fatalf("bank directory read: %d %s", bank.Code, bank.Body.String())
		}
		bankPatch := request(http.MethodPatch, "/v1/banks/10000000-0000-4000-8000-000000000001", `{"name":"Resource Test Bank"}`, "resource-bank-patch")
		if bankPatch.Code != http.StatusOK {
			t.Fatalf("bank patch: %d %s", bankPatch.Code, bankPatch.Body.String())
		}
		product := request(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/card-products", `{"product_code":"RESOURCE_TEST","name":"Resource Test","configuration":{}}`, "resource-product")
		if product.Code != http.StatusCreated {
			t.Fatalf("product create: %d %s", product.Code, product.Body.String())
		}
		client := request(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/clients", `{"external_client_ref":"resource-client","display_name":"Resource Client"}`, "resource-client")
		if client.Code != http.StatusCreated {
			t.Fatalf("client create: %d %s", client.Code, client.Body.String())
		}
		id := func(body string) string {
			var v map[string]any
			if err := json.Unmarshal([]byte(body), &v); err != nil {
				t.Fatal(err)
			}
			value, _ := v["id"].(string)
			if value == "" {
				t.Fatalf("missing id: %s", body)
			}
			return value
		}
		productID, clientID := id(product.Body.String()), id(client.Body.String())
		productGet := request(http.MethodGet, "/v1/banks/10000000-0000-4000-8000-000000000001/card-products/"+productID, "", "")
		if productGet.Code != http.StatusOK {
			t.Fatalf("product read: %d %s", productGet.Code, productGet.Body.String())
		}
		account := request(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/account-references", `{"client_id":"`+clientID+`","external_account_ref":"resource-account"}`, "resource-account")
		if account.Code != http.StatusCreated {
			t.Fatalf("account create: %d %s", account.Code, account.Body.String())
		}
		accountID := id(account.Body.String())
		issued := request(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/cards", `{"client_id":"`+clientID+`","account_reference_id":"`+accountID+`","product_id":"`+productID+`","reason":"resource test"}`, "resource-issue")
		if issued.Code != http.StatusCreated || strings.Contains(issued.Body.String(), "credential_reference") {
			t.Fatalf("issue response: %d %s", issued.Code, issued.Body.String())
		}
		var issue map[string]map[string]any
		if err := json.Unmarshal(issued.Body.Bytes(), &issue); err != nil {
			t.Fatal(err)
		}
		cardID, _ := issue["card"]["id"].(string)
		if cardID == "" {
			t.Fatalf("missing issued card: %s", issued.Body.String())
		}
		activated := request(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/cards/"+cardID+":activate", `{"reason":"resource activation"}`, "resource-activate")
		if activated.Code != http.StatusOK {
			t.Fatalf("activate: %d %s", activated.Code, activated.Body.String())
		}
		readonly, _, _, err := service.Login(ctx, "issuer_readonly", "Test-Issuer-Readonly!2026", "90000000-0000-4000-8000-000000000102")
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/banks/10000000-0000-4000-8000-000000000001/cards/"+cardID+":suspend", strings.NewReader(`{"reason":"denied"}`))
		r.Header.Set("Authorization", "Bearer "+readonly.AccessToken)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "readonly-denied")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("readonly mutation: %d %s", w.Code, w.Body.String())
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
		mustSQL("postgres", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=:'database_name';", "database_name="+shardDB)
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
