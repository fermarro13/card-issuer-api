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
	"card-issuer-api/internal/bank"
	"card-issuer-api/internal/batch"
	"card-issuer-api/internal/card"
	"card-issuer-api/internal/catalog"
	"card-issuer-api/internal/database"
	"card-issuer-api/internal/executor"
	"card-issuer-api/internal/idempotency"
	authrepository "card-issuer-api/internal/repository/auth"
	controlrepository "card-issuer-api/internal/repository/control"
	routingrepository "card-issuer-api/internal/repository/routing"
	shardrepository "card-issuer-api/internal/repository/shard"
	"card-issuer-api/internal/server"
	"card-issuer-api/internal/staff"
	"card-issuer-api/internal/tenant"
	"card-issuer-api/internal/vault"
)

func testVault(t *testing.T) *vault.InMemory {
	t.Helper()
	credentialVault, err := vault.NewInMemory("test", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return credentialVault
}

func testFingerprinter(t *testing.T) *idempotency.Fingerprinter {
	t.Helper()
	fingerprinter, err := idempotency.NewFingerprinter(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return fingerprinter
}

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
	t.Setenv("EXECUTOR_DB_PASSWORD", "test-executor ' $ & @ / : password")
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
	executorControl, err := database.Open(ctx, urlFor("ci_app_executor", os.Getenv("EXECUTOR_DB_PASSWORD"), controlDB))
	if err != nil {
		t.Fatal(err)
	}
	defer executorControl.Close()
	executorShard, err := database.Open(ctx, urlFor("ci_app_executor", os.Getenv("EXECUTOR_DB_PASSWORD"), shardDB))
	if err != nil {
		t.Fatal(err)
	}
	defer executorShard.Close()
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
		if err := executorControl.QueryRow(ctx, "SELECT count(*) FROM control.bank_routing_entries").Scan(&count); err != nil || count != 1 {
			t.Fatal("executor cannot read routing")
		}
		if _, err := controlrepository.New(executorControl).User(ctx, "20000000-0000-4000-8000-000000000001"); err != nil {
			t.Fatalf("executor cannot use the approved directory user lookup: %v", err)
		}
		for _, query := range []string{"SELECT password_hash FROM control.users", "SELECT * FROM control.auth_sessions", "UPDATE control.users SET status='disabled'"} {
			if _, err := executorControl.Exec(ctx, query); err == nil {
				t.Fatalf("executor control login accepted forbidden statement: %s", query)
			}
		}
		if _, err := executorShard.Exec(ctx, "BEGIN; SELECT set_config('app.entity_id','10000000-0000-4000-8000-000000000001',true); SELECT count(*) FROM bank.cards; ROLLBACK;"); err != nil {
			t.Fatal("executor cannot establish tenant context")
		}
		for _, query := range []string{"SET ROLE ci_owner", "DELETE FROM bank.cards", "SELECT * FROM bank.idempotency_records"} {
			if _, err := executorShard.Exec(ctx, query); err == nil {
				t.Fatalf("executor shard login accepted forbidden statement: %s", query)
			}
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
		routes := routingrepository.New(control)
		fingerprinter := testFingerprinter(t)
		handler := server.Handler(authPool.Ping, control.Ping, shard.Ping, service, server.NewResourceHandler(service, server.Dependencies{
			Bank:    bank.New(controlrepository.NewBank(authPool), routes, shardrepository.NewBank(shard), "shard_01", fingerprinter),
			Access:  tenant.New(routes, "shard_01"),
			Catalog: catalog.New(shardrepository.NewCatalog(shard), fingerprinter),
			Card:    card.New(shardrepository.NewCard(shard), testVault(t), fingerprinter),
			Batch:   batch.New(shardrepository.NewBatch(shard), fingerprinter),
			Staff:   staff.New(controlrepository.NewStaff(authPool), routes, "shard_01", fingerprinter),
		}, []byte("integration-cursor-key")))
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
			t.Fatalf("bank provision replay: status=%d first=%q replay=%q", replayedProvision.Code, provisioned.Body.String(), replayedProvision.Body.String())
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
	t.Run("executor_postgresql_acceptance", func(t *testing.T) {
		const bankID = "10000000-0000-4000-8000-000000000001"
		shardSQL := func(statement string) string {
			t.Helper()
			out, err := sql(shardDB, "BEGIN; "+statement+" COMMIT;")
			if err != nil {
				t.Fatalf("executor fixture SQL failed: %v %s", err, out)
			}
			return out
		}
		activeCardID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT id::text FROM bank.cards WHERE status='active' ORDER BY created_at DESC LIMIT 1;")
		if activeCardID == "" {
			t.Fatal("public API fixture did not leave an active card for executor acceptance")
		}
		expiryCardID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.cards(entity_id,client_id,account_reference_id,product_id,status,created_by,updated_by) SELECT '" + bankID + "',c.id,a.id,p.id,'issued','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001' FROM bank.clients c JOIN bank.account_references a ON a.entity_id=c.entity_id AND a.client_id=c.id JOIN bank.card_products p ON p.entity_id=c.entity_id WHERE c.entity_id='" + bankID + "' AND c.external_client_ref='resource-client' AND p.product_code='RESOURCE_TEST' LIMIT 1 RETURNING id::text;")
		if expiryCardID == "" {
			t.Fatal("could not create expiry retry fixture")
		}

		batchCreatedAt := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.idempotency_records(entity_id,operation_scope,idempotency_key,request_fingerprint,expires_at,created_by,updated_by) VALUES ('" + bankID + "','executor_acceptance','executor-acceptance-key',decode(repeat('00',32),'hex'),clock_timestamp()+interval '1 day','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		batchID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batches(entity_id,target_status,reason,requested_by,requester_role,request_id,idempotency_record_id,item_count,created_by,updated_by) VALUES ('" + bankID + "','suspended','executor acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'" + batchCreatedAt + "',1,'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		opID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES ('" + bankID + "','" + activeCardID + "','suspend','queued','executor acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES ('" + bankID + "','" + batchID + "','" + activeCardID + "','" + opID + "'); UPDATE bank.card_status_batches SET status='queued',next_attempt_at=clock_timestamp() WHERE entity_id='" + bankID + "' AND id='" + batchID + "';")

		store := shardrepository.NewExecutor(executorShard)
		claim := executor.Claim{BankID: bankID, LeaseDuration: 10 * time.Millisecond}
		type result struct {
			batch *executor.Batch
			owner string
			err   error
		}
		claims := make(chan result, 2)
		for _, owner := range []string{"executor-a", "executor-b"} {
			go func(owner string) {
				batch, err := store.ClaimBatch(ctx, executor.Claim{BankID: bankID, Owner: owner, LeaseDuration: claim.LeaseDuration})
				claims <- result{batch, owner, err}
			}(owner)
		}
		var initial result
		for range 2 {
			got := <-claims
			if got.err != nil {
				t.Fatalf("concurrent claim: %v", got.err)
			}
			if got.batch != nil {
				if initial.batch != nil {
					t.Fatal("concurrent executors claimed the same batch")
				}
				initial = got
			}
		}
		if initial.batch == nil {
			t.Fatal("no executor claimed queued batch")
		}
		time.Sleep(20 * time.Millisecond)
		if recovered, err := store.RecoverBatches(ctx, executor.Claim{BankID: bankID}); err != nil || recovered != 1 {
			t.Fatalf("lease recovery = %d, %v", recovered, err)
		}
		if err := store.ApplyBatch(ctx, *initial.batch, initial.owner); !errors.Is(err, executor.ErrStale) {
			t.Fatalf("stale worker applied recovered batch: %v", err)
		}
		fresh, err := store.ClaimBatch(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
		if err != nil || fresh == nil {
			t.Fatalf("claim recovered batch: %v", err)
		}
		if err := store.ApplyBatch(ctx, *fresh, "executor-c"); err != nil {
			t.Fatalf("apply recovered batch: %v", err)
		}
		if err := store.ApplyBatch(ctx, *fresh, "executor-c"); !errors.Is(err, executor.ErrStale) {
			t.Fatalf("lost acknowledgement duplicated application: %v", err)
		}
		if histories := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT count(*) FROM bank.card_status_history WHERE operation_id='" + opID + "';"); histories != "1" {
			t.Fatalf("duplicate batch history: %s", histories)
		}
		ignoredIdempotencyID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.idempotency_records(entity_id,operation_scope,idempotency_key,request_fingerprint,expires_at,created_by,updated_by) VALUES ('" + bankID + "','executor_ignored','executor-ignored-key',decode(repeat('01',32),'hex'),clock_timestamp()+interval '1 day','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		validIgnoredCardID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.cards(entity_id,client_id,account_reference_id,product_id,status,activated_at,created_by,updated_by) SELECT '" + bankID + "',c.id,a.id,p.id,'active',clock_timestamp(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001' FROM bank.clients c JOIN bank.account_references a ON a.entity_id=c.entity_id AND a.client_id=c.id JOIN bank.card_products p ON p.entity_id=c.entity_id WHERE c.entity_id='" + bankID + "' AND c.external_client_ref='resource-client' AND p.product_code='RESOURCE_TEST' LIMIT 1 RETURNING id::text;")
		ignoredBatchID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batches(entity_id,target_status,reason,requested_by,requester_role,request_id,idempotency_record_id,item_count,created_by,updated_by) VALUES ('" + bankID + "','suspended','executor ignored acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'" + ignoredIdempotencyID + "',2,'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		ignoredOpID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES ('" + bankID + "','" + activeCardID + "','suspend','queued','executor ignored acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		validIgnoredOpID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES ('" + bankID + "','" + validIgnoredCardID + "','suspend','queued','executor ignored acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES ('" + bankID + "','" + ignoredBatchID + "','" + activeCardID + "','" + ignoredOpID + "'),('" + bankID + "','" + ignoredBatchID + "','" + validIgnoredCardID + "','" + validIgnoredOpID + "'); UPDATE bank.card_status_batches SET status='queued',next_attempt_at=clock_timestamp() WHERE entity_id='" + bankID + "' AND id='" + ignoredBatchID + "';")
		ignoredBatch, err := store.ClaimBatch(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
		if err != nil || ignoredBatch == nil {
			t.Fatalf("claim ignored batch: %v", err)
		}
		if err = store.ApplyBatch(ctx, *ignoredBatch, "executor-c"); err != nil {
			t.Fatalf("apply ignored batch: %v", err)
		}
		if outcome := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT applied_count::text||':'||ignored_count FROM bank.card_status_batches WHERE id='" + ignoredBatchID + "';"); outcome != "1:1" {
			t.Fatalf("ignored batch operation outcome: %s", outcome)
		}
		if outcome := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT i.outcome||':'||o.status||':'||(o.completed_at IS NOT NULL)::text FROM bank.card_status_batch_items i JOIN bank.card_operations o ON o.entity_id=i.entity_id AND o.id=i.operation_id WHERE i.operation_id='" + ignoredOpID + "';"); outcome != "ignored:succeeded:true" {
			t.Fatalf("ignored item operation outcome: %s", outcome)
		}
		if outcome := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT c.status||':'||i.outcome||':'||o.status FROM bank.cards c JOIN bank.card_status_batch_items i ON i.entity_id=c.entity_id AND i.card_id=c.id JOIN bank.card_operations o ON o.entity_id=i.entity_id AND o.id=i.operation_id WHERE i.operation_id='" + validIgnoredOpID + "';"); outcome != "suspended:applied:succeeded" {
			t.Fatalf("multi-card applied item outcome: %s", outcome)
		}
		if histories := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT count(*) FROM bank.card_status_history WHERE operation_id='" + ignoredOpID + "';"); histories != "0" {
			t.Fatalf("ignored batch wrote history: %s", histories)
		}
		rollbackIdempotencyID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.idempotency_records(entity_id,operation_scope,idempotency_key,request_fingerprint,expires_at,created_by,updated_by) VALUES ('" + bankID + "','executor_atomic','executor-atomic-key',decode(repeat('02',32),'hex'),clock_timestamp()+interval '1 day','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		rollbackCardID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.cards(entity_id,client_id,account_reference_id,product_id,status,activated_at,created_by,updated_by) SELECT '" + bankID + "',c.id,a.id,p.id,'active',clock_timestamp(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001' FROM bank.clients c JOIN bank.account_references a ON a.entity_id=c.entity_id AND a.client_id=c.id JOIN bank.card_products p ON p.entity_id=c.entity_id WHERE c.entity_id='" + bankID + "' AND c.external_client_ref='resource-client' AND p.product_code='RESOURCE_TEST' LIMIT 1 RETURNING id::text;")
		rollbackBatchID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batches(entity_id,target_status,reason,requested_by,requester_role,request_id,idempotency_record_id,item_count,created_by,updated_by) VALUES ('" + bankID + "','suspended','executor atomic acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'" + rollbackIdempotencyID + "',2,'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		rollbackValidOpID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES ('" + bankID + "','" + rollbackCardID + "','suspend','queued','executor atomic acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		rollbackInvalidOpID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_operations(entity_id,card_id,action,status,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES ('" + bankID + "','" + expiryCardID + "','suspend','queued','executor atomic acceptance','20000000-0000-4000-8000-000000000001','issuer_operator',gen_random_uuid(),'20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001') RETURNING id::text;")
		shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES ('" + bankID + "','" + rollbackBatchID + "','" + rollbackCardID + "','" + rollbackValidOpID + "'),('" + bankID + "','" + rollbackBatchID + "','" + expiryCardID + "','" + rollbackInvalidOpID + "'); UPDATE bank.card_status_batches SET status='queued',next_attempt_at=clock_timestamp() WHERE entity_id='" + bankID + "' AND id='" + rollbackBatchID + "';")
		rollbackBatch, err := store.ClaimBatch(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
		if err != nil || rollbackBatch == nil {
			t.Fatalf("claim atomic rollback batch: %v", err)
		}
		if err = store.ApplyBatch(ctx, *rollbackBatch, "executor-c"); err == nil {
			t.Fatal("invalid multi-card batch was applied")
		}
		if state := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT c.status||':'||o.status FROM bank.cards c JOIN bank.card_operations o ON o.entity_id=c.entity_id AND o.card_id=c.id WHERE o.id='" + rollbackValidOpID + "';"); state != "active:queued" {
			t.Fatalf("invalid batch was not atomic: %s", state)
		}

		shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; UPDATE bank.cards SET expires_at=clock_timestamp()-interval '1 minute' WHERE id IN ('" + activeCardID + "','" + expiryCardID + "');")
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if err := store.EnsureExpiryRun(ctx, bankID, today, "executor-c"); err != nil {
			t.Fatalf("ensure expiry run: %v", err)
		}
		completed, err := store.ClaimExpiryItem(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
		if err != nil || completed == nil {
			t.Fatalf("claim expiry item: %v", err)
		}
		if err := store.ApplyExpiryItem(ctx, *completed, "executor-c"); err != nil {
			t.Fatalf("apply expiry item: %v", err)
		}
		var retry *executor.ExpiryItem
		for attempt := 0; attempt < 4; attempt++ {
			retry, err = store.ClaimExpiryItem(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
			if err != nil || retry == nil {
				t.Fatalf("claim expiry retry %d: %v", attempt, err)
			}
			if err := store.RequeueExpiryItem(ctx, *retry, "executor-c", 0); err != nil {
				t.Fatalf("requeue expiry retry %d: %v", attempt, err)
			}
		}
		if aggregate := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT expired_count::text||','||manual_retry_required_count::text FROM bank.card_expiry_runs WHERE id='" + retry.RunID + "';"); aggregate != "1,1" {
			t.Fatalf("expiry aggregate = %s", aggregate)
		}
		retryService := card.New(shardrepository.NewCard(shard), testVault(t), testFingerprinter(t))
		principal := auth.Principal{UserID: "20000000-0000-4000-8000-000000000001", Role: "issuer_operator"}
		for range 2 {
			if err := retryService.RetryExpiryWorkflow(ctx, principal, bankID, retry.RunID, retry.ID, "executor-manual-retry", []byte(`{"reason":"manual retry"}`), "manual retry", "90000000-0000-4000-8000-000000000199"); err != nil {
				t.Fatalf("idempotent manual retry: %v", err)
			}
		}
		if count := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT manual_retry_count FROM bank.card_expiry_run_items WHERE id='" + retry.ID + "';"); count != "1" {
			t.Fatalf("manual retry count = %s", count)
		}
		manualRetry, err := store.ClaimExpiryItem(ctx, executor.Claim{BankID: bankID, Owner: "executor-c", LeaseDuration: time.Second})
		if err != nil || manualRetry == nil {
			t.Fatalf("claim manually retried expiry item: %v", err)
		}
		if err = store.ApplyExpiryItem(ctx, *manualRetry, "executor-c"); err != nil {
			t.Fatalf("apply manually retried expiry item: %v", err)
		}
		lateExpiryCardID := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; INSERT INTO bank.cards(entity_id,client_id,account_reference_id,product_id,status,created_by,updated_by,expires_at) SELECT '" + bankID + "',c.id,a.id,p.id,'issued','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001',clock_timestamp()-interval '1 minute' FROM bank.clients c JOIN bank.account_references a ON a.entity_id=c.entity_id AND a.client_id=c.id JOIN bank.card_products p ON p.entity_id=c.entity_id WHERE c.entity_id='" + bankID + "' AND c.external_client_ref='resource-client' AND p.product_code='RESOURCE_TEST' LIMIT 1 RETURNING id::text;")
		if lateExpiryCardID == "" {
			t.Fatal("could not create late expiry fixture")
		}
		if err = store.EnsureExpiryRun(ctx, bankID, today, "executor-c"); err != nil {
			t.Fatalf("schedule late card after completed expiry run: %v", err)
		}
		if state := shardSQL("SET ROLE ci_owner; SET LOCAL app.entity_id='" + bankID + "'; SELECT status||':'||item_count FROM bank.card_expiry_runs WHERE id='" + retry.RunID + "';"); state != "processing:3" {
			t.Fatalf("late expiry scheduling state = %s", state)
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
