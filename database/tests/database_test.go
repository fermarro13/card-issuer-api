package database_test

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func TestMain(m *testing.M) {
	if runtime.Version() != "go1.27.1" {
		fmt.Fprintln(os.Stderr, "Tests require Go 1.27.1; found", runtime.Version())
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestPasswordFixtures(t *testing.T) {
	seed, err := os.ReadFile("../seeds/control.sql")
	if err != nil {
		t.Fatal(err)
	}
	docs, err := os.ReadFile("../TEST-CREDENTIALS.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|[^|]+\\| `([^`]+)` \\|").FindAllStringSubmatch(string(docs), -1)
	if len(rows) != 4 {
		t.Fatalf("expected four documented accounts; found %d", len(rows))
	}
	salts := map[string]bool{}
	for _, row := range rows {
		t.Run(row[1], func(t *testing.T) {
			pattern := regexp.MustCompile("'" + row[1] + "','(\\$argon2id\\$[^']+)'")
			match := pattern.FindStringSubmatch(string(seed))
			if match == nil {
				t.Fatal("missing seed hash")
			}
			fields := strings.Split(match[1], "$")
			if len(fields) != 6 || fields[2] != "v=19" || fields[3] != "m=19456,t=2,p=1" {
				t.Fatal("unexpected Argon2 parameters")
			}
			salt, err := base64.RawStdEncoding.DecodeString(fields[4])
			if err != nil {
				t.Fatal(err)
			}
			hash, err := base64.RawStdEncoding.DecodeString(fields[5])
			if err != nil {
				t.Fatal(err)
			}
			if len(salt) != 16 || len(hash) != 32 || salts[fields[4]] {
				t.Fatal("salt/hash length or salt independence failed")
			}
			salts[fields[4]] = true
			actual := argon2.IDKey([]byte(row[2]), salt, 2, 19456, 1, 32)
			if subtle.ConstantTimeCompare(hash, actual) != 1 {
				t.Fatal("documented password does not verify")
			}
			if bytes.Equal(hash, argon2.IDKey([]byte("incorrect-password"), salt, 2, 19456, 1, 32)) {
				t.Fatal("incorrect password verified")
			}
		})
	}
}

type harness struct {
	t                                           *testing.T
	psql, runner, runnerCommand, control, shard string
}

func (h harness) sql(db, statement string) (string, error) {
	cmd := exec.Command(h.psql, "-X", "-w", "-qAt", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=sqlstate", "-d", db)
	cmd.Stdin = strings.NewReader(statement)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
func (h harness) must(db, statement string) string {
	h.t.Helper()
	out, err := h.sql(db, statement)
	if err != nil {
		h.t.Fatalf("SQL failed: %v\n%s", err, out)
	}
	return out
}
func (h harness) equal(db, statement, want string) {
	h.t.Helper()
	if got := h.must(db, statement); got != want {
		h.t.Fatalf("query result = %q; want %q", got, want)
	}
}
func (h harness) reject(db, statement, state string) {
	h.t.Helper()
	out, err := h.sql(db, statement)
	if err == nil || !strings.Contains(out, state) {
		h.t.Fatalf("expected SQLSTATE %s; error=%v output=%s", state, err, out)
	}
}
func (h harness) run(action string, extra ...string) (string, error) {
	var args []string
	if h.runnerCommand == "pwsh" {
		args = []string{"-NoProfile", "-File", h.runner, "-Action", action, "-Psql", h.psql, "-ControlDatabase", h.control, "-ShardDatabase", h.shard}
		args = append(args, extra...)
	} else {
		args = []string{h.runner, "--action", action, "--psql", h.psql, "--control-database", h.control, "--shard-database", h.shard}
		for _, value := range extra {
			if value == "-MigrationRoot" {
				value = "--migration-root"
			}
			args = append(args, value)
		}
	}
	out, err := exec.Command(h.runnerCommand, args...).CombinedOutput()
	return string(out), err
}
func (h harness) setup() {
	h.t.Helper()
	if out, err := h.run("SetupTest"); err != nil {
		h.t.Fatalf("setup failed: %v\n%s", err, out)
	}
}
func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDatabaseIntegration(t *testing.T) {
	if os.Getenv("CI_TEST_DATABASE") != "1" {
		t.Skip("set CI_TEST_DATABASE=1 with an administrative connection to a disposable PostgreSQL 17 instance")
	}
	psql := os.Getenv("PSQL")
	if psql == "" {
		psql = "psql"
	}
	psql, err := exec.LookPath(psql)
	if err != nil {
		t.Fatal(err)
	}
	runnerName, runnerCommand := "../Invoke-Database.ps1", "pwsh"
	if runtime.GOOS != "windows" {
		runnerName, runnerCommand = "../Invoke-Database.sh", "sh"
	}
	if _, err := exec.LookPath(runnerCommand); err != nil {
		t.Fatal(err)
	}
	runner, err := filepath.Abs(runnerName)
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("ci_test_%d", time.Now().UnixNano())
	h := harness{t: t, psql: psql, runner: runner, runnerCommand: runnerCommand, control: prefix + "_control", shard: prefix + "_shard"}
	if version := h.must("postgres", "SHOW server_version_num;"); !strings.HasPrefix(version, "17") {
		t.Fatalf("expected PostgreSQL 17; got %s", version)
	}
	t.Cleanup(func() {
		for _, db := range []string{h.control, h.shard} {
			if !strings.HasPrefix(db, prefix+"_") {
				t.Error("unsafe cleanup target")
				continue
			}
			if out, err := h.sql("postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE);`, db)); err != nil {
				t.Errorf("cleanup: %v %s", err, out)
			}
		}
	})
	h.setup()
	t.Run("bootstrap_refuses_existing_owner", func(t *testing.T) {
		h := h
		h.t = t
		h.control += "_owner"
		h.shard += "_owner"
		h.must("postgres", fmt.Sprintf(`CREATE DATABASE %q;`, h.control))
		t.Cleanup(func() {
			for _, db := range []string{h.control, h.shard} {
				if !strings.HasPrefix(db, prefix+"_") {
					t.Error("unsafe cleanup target")
					continue
				}
				if out, err := h.sql("postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE);`, db)); err != nil {
					t.Errorf("cleanup: %v %s", err, out)
				}
			}
		})
		if out, err := h.run("Bootstrap"); err == nil || !strings.Contains(out, "refusing takeover") {
			t.Fatalf("bootstrap did not fail safely: %v %s", err, out)
		}
		h.equal("postgres", fmt.Sprintf("SELECT datdba='ci_owner'::regrole FROM pg_database WHERE datname='%s';", h.control), "f")
	})
	t.Run("fresh_schema_and_seeds", func(t *testing.T) {
		h := h
		h.t = t
		h.equal(h.control, "SELECT count(*) FROM control.users;", "4")
		h.equal(h.control, "SELECT count(*) FROM control.users WHERE status='enabled' AND normalized_username=role AND ((role LIKE 'bank_%')=(entity_id IS NOT NULL));", "4")
		h.equal(h.control, "SELECT count(*) FROM control.auth_sessions;", "0")
		h.equal(h.shard, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='bank' AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity;", "15")
		h.equal(h.control, "SELECT count(*) FROM pg_tables WHERE schemaname='control';", "6")
		h.equal(h.control, "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_owner','ci_auth_runtime','ci_routing_reader','ci_business_runtime','ci_executor_runtime') AND NOT (rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb);", "5")
	})
	t.Run("seed_replay_preserves_changes", func(t *testing.T) {
		h := h
		h.t = t
		h.must(h.control, "UPDATE control.users SET status='disabled',role='issuer_readonly',entity_id=NULL,password_hash=(SELECT password_hash FROM control.users WHERE normalized_username='issuer_readonly') WHERE normalized_username='bank_operator';")
		before := h.must(h.control, "SELECT status||role||md5(password_hash)||auth_version FROM control.users WHERE normalized_username='bank_operator';")
		h.setup()
		h.equal(h.control, "SELECT status||role||md5(password_hash)||auth_version FROM control.users WHERE normalized_username='bank_operator';", before)
		h.equal(h.control, "SELECT count(*) FROM control.authentication_audit_events;", "4")
		h.equal(h.shard, "SELECT count(*) FROM bank.audit_events;", "1")
		h.must(h.control, "UPDATE control.users SET status='enabled',role='bank_operator',entity_id='10000000-0000-4000-8000-000000000001' WHERE normalized_username='bank_operator';")
	})
	t.Run("concurrent_runners", func(t *testing.T) {
		h := h
		h.t = t
		h.control += "_race"
		h.shard += "_race"
		t.Cleanup(func() {
			for _, db := range []string{h.control, h.shard} {
				if !strings.HasPrefix(db, prefix+"_") {
					t.Error("unsafe cleanup target")
					continue
				}
				if out, err := h.sql("postgres", fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE);`, db)); err != nil {
					t.Errorf("cleanup: %v %s", err, out)
				}
			}
		})
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if out, err := h.run("SetupTest"); err != nil {
					t.Errorf("concurrent setup: %v %s", err, out)
				}
			}()
		}
		wg.Wait()
		h.equal(h.control, "SELECT count(*) FROM ci_meta.schema_migrations;", "3")
		h.equal(h.shard, "SELECT count(*) FROM ci_meta.schema_migrations;", "5")
	})
	t.Run("migration_checksums_and_rollback", func(t *testing.T) {
		h := h
		h.t = t
		root := t.TempDir()
		for _, kind := range []string{"control", "shard"} {
			if err := os.Mkdir(filepath.Join(root, kind), 0700); err != nil {
				t.Fatal(err)
			}
			names := []string{"001_initial.sql", "002_public_resource_api.sql", "004_executor_runtime.sql"}
			if kind == "shard" {
				names = append(names, "005_executor_runtime_hardening.sql")
			}
			for _, name := range names {
				data := read(t, "../migrations/"+kind+"/"+name)
				if err := os.WriteFile(filepath.Join(root, kind, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
		path := filepath.Join(root, "control", "001_initial.sql")
		original := read(t, path)
		if err := os.WriteFile(path, []byte(original+"\n-- changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := h.run("Migrate", "-MigrationRoot", root); err == nil || !strings.Contains(out, "Applied migration changed") {
			t.Fatalf("checksum rejection failed: %v %s", err, out)
		}
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "control", "002_failure.sql"), []byte("CREATE TABLE control.must_rollback(id int); SELECT 1/0;"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.run("Migrate", "-MigrationRoot", root); err == nil {
			t.Fatal("failing migration accepted")
		}
		h.equal(h.control, "SELECT to_regclass('control.must_rollback') IS NULL;", "t")
		h.equal(h.control, "SELECT count(*) FROM ci_meta.schema_migrations;", "3")
	})
	t.Run("interrupted_seed_and_collision", func(t *testing.T) {
		h := h
		h.t = t
		// A committed bank with no central directory is a legitimate interrupted setup.
		h.must(h.control, "DELETE FROM control.authentication_audit_events; DELETE FROM control.users; DELETE FROM control.bank_routing_entries;")
		h.setup()
		h.equal(h.control, "SELECT count(*) FROM control.users;", "4")
		h.must(h.control, "UPDATE control.users SET normalized_username='collision' WHERE normalized_username='issuer_readonly';")
		if out, err := h.run("SetupTest"); err == nil || !strings.Contains(out, "identity collision") {
			t.Fatalf("identity collision not rejected: %v %s", err, out)
		}
		h.must(h.control, "UPDATE control.users SET normalized_username='issuer_readonly' WHERE normalized_username='collision'; UPDATE control.bank_routing_entries SET shard_id='another_shard';")
		if _, err := h.run("SetupTest"); err == nil {
			t.Fatal("seed silently rerouted bank")
		}
		h.equal(h.control, "SELECT shard_id FROM control.bank_routing_entries;", "another_shard")
		h.must(h.control, "UPDATE control.bank_routing_entries SET shard_id='shard_01';")
	})
	t.Run("authentication_constraints", func(t *testing.T) {
		h := h
		h.t = t
		h.must(h.control, read(t, "../verification/control.sql"))
	})
	h.must(h.shard, read(t, "../verification/shard-fixtures.sql"))
	t.Run("tenant_constraints_and_permissions", func(t *testing.T) {
		h := h
		h.t = t
		h.must(h.shard, read(t, "../verification/shard.sql"))
		tenant := "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001'; "
		for _, tc := range []struct{ name, sql, state string }{
			{"wrong client", "UPDATE bank.cards SET client_id='30000000-0000-4000-8000-000000000002' WHERE id='60000000-0000-4000-8000-000000000001';", "23503"},
			{"cross bank write", "INSERT INTO bank.clients(entity_id,external_client_ref,created_by,updated_by) VALUES ('10000000-0000-4000-8000-000000000002','bad','20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001');", "42501"},
			{"ownership mutation", "UPDATE bank.clients SET entity_id='10000000-0000-4000-8000-000000000002';", "P0001"},
			{"history update", "UPDATE bank.card_status_history SET reason='rewritten';", "42501"},
			{"audit delete", "DELETE FROM bank.audit_events;", "42501"},
			{"audit truncate", "TRUNCATE bank.audit_events;", "42501"},
			{"migration access", "SELECT * FROM ci_meta.schema_migrations;", "42501"},
			{"invalid state", "UPDATE bank.cards SET status='invented';", "23514"},
			{"operation attribution", "UPDATE bank.card_operations SET actor_user_id='20000000-0000-4000-8000-000000000002';", "P0001"},
			{"batch request mutation", "UPDATE bank.card_status_batches SET target_status='closed';", "P0001"},
			{"wrong history card", "INSERT INTO bank.card_status_history(entity_id,card_id,operation_id,previous_status,new_status,reason,actor_user_id,actor_role) VALUES ('10000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000002','70000000-0000-4000-8000-000000000001','issued','active','test','20000000-0000-4000-8000-000000000001','issuer_operator');", "23503"},
			{"wrong batch operation", "INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES ('10000000-0000-4000-8000-000000000001','80000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000002','70000000-0000-4000-8000-000000000001');", "23503"},
		} {
			t.Run(tc.name, func(t *testing.T) { h := h; h.t = t; h.reject(h.shard, tenant+tc.sql, tc.state) })
		}
		h.must(h.shard, tenant+"UPDATE bank.card_operations SET status='processing',executor_identity='test-worker'; ROLLBACK;")
		h.reject(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='malformed'; SELECT * FROM bank.clients;", "22P02")
		h.reject(h.control, "SET ROLE ci_routing_reader; SELECT password_hash FROM control.users;", "42501")
		h.reject(h.control, "SET ROLE ci_business_runtime; SELECT * FROM control.users;", "42501")
		h.reject(h.control, "SET ROLE ci_auth_runtime; UPDATE control.authentication_audit_events SET event_type='rewritten';", "42501")
		executorTenant := "BEGIN; SET LOCAL ROLE ci_executor_runtime; SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001'; "
		for _, tc := range []struct{ name, sql, state string }{
			{"ungranted table", "SELECT * FROM bank.clients;", "42501"},
			{"ungranted card column", "UPDATE bank.cards SET client_id='30000000-0000-4000-8000-000000000001' WHERE id='60000000-0000-4000-8000-000000000001';", "42501"},
			{"cross tenant insert", "INSERT INTO bank.card_expiry_runs(entity_id,run_date,executor_identity) VALUES ('10000000-0000-4000-8000-000000000002',CURRENT_DATE,'executor-test');", "42501"},
		} {
			t.Run("executor_"+tc.name, func(t *testing.T) { h := h; h.t = t; h.reject(h.shard, executorTenant+tc.sql, tc.state) })
		}
		h.reject(h.control, "SET ROLE ci_executor_runtime; SELECT password_hash FROM control.users;", "42501")
	})
	t.Run("atomic_database_rollback", func(t *testing.T) {
		h := h
		h.t = t
		h.reject(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001'; UPDATE bank.cards SET status='active',version=version+1 WHERE id='60000000-0000-4000-8000-000000000001'; UPDATE bank.cards SET status='invalid'; COMMIT;", "23514")
		h.equal(h.shard, "SELECT status||':'||version FROM bank.cards WHERE id='60000000-0000-4000-8000-000000000001';", "issued:1")
		h.equal(h.shard, "SELECT status FROM bank.card_status_batches;", "draft")
		h.equal(h.shard, "SELECT status FROM bank.card_operations;", "queued")
		h.equal(h.shard, "SELECT count(*) FROM bank.card_status_batch_items;", "0")
	})
	t.Run("batch_drafts_and_individual_expiry_runs", func(t *testing.T) {
		h := h
		h.t = t
		entity := "10000000-0000-4000-8000-000000000001"
		user := "20000000-0000-4000-8000-000000000001"
		card := "60000000-0000-4000-8000-000000000002"
		batch := "81000000-0000-4000-8000-000000000001"
		retryBatch := "81000000-0000-4000-8000-000000000002"
		operation := "71000000-0000-4000-8000-000000000002"
		expiryOperation := "71000000-0000-4000-8000-000000000003"
		expiryRun := "82000000-0000-4000-8000-000000000001"
		expiryItem := "83000000-0000-4000-8000-000000000001"
		setup := "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='" + entity + "'; " +
			"INSERT INTO bank.idempotency_records(entity_id,id,operation_scope,idempotency_key,request_fingerprint,expires_at,created_by,updated_by) VALUES " +
			"('" + entity + "','91000000-0000-4000-8000-000000000001','status_batch','draft-key',decode(repeat('02',32),'hex'),now()+interval '1 day','" + user + "','" + user + "')," +
			"('" + entity + "','91000000-0000-4000-8000-000000000002','status_batch','retry-key',decode(repeat('03',32),'hex'),now()+interval '1 day','" + user + "','" + user + "'); " +
			"INSERT INTO bank.card_status_batches(entity_id,id,target_status,reason,requested_by,requester_role,request_id,idempotency_record_id,status,item_count,created_by,updated_by) VALUES " +
			"('" + entity + "','" + batch + "','suspended','test retry','" + user + "','issuer_operator',gen_random_uuid(),'91000000-0000-4000-8000-000000000001','draft',1,'" + user + "','" + user + "'); " +
			"INSERT INTO bank.card_operations(entity_id,id,card_id,action,reason,actor_user_id,actor_role,request_id,created_by,updated_by) VALUES " +
			"('" + entity + "','" + operation + "','" + card + "','suspend','test','" + user + "','issuer_operator',gen_random_uuid(),'" + user + "','" + user + "'); " +
			"INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id) VALUES " +
			"('" + entity + "','" + batch + "','" + card + "','" + operation + "'); " +
			"UPDATE bank.card_status_batches SET status='queued' WHERE id='" + batch + "'; " +
			"UPDATE bank.card_status_batches SET status='cancelled',completed_at=now() WHERE id='" + batch + "'; " +
			"INSERT INTO bank.card_status_batches(entity_id,id,target_status,reason,requested_by,requester_role,request_id,idempotency_record_id,status,item_count,retry_of_batch_id,created_by,updated_by) VALUES " +
			"('" + entity + "','" + retryBatch + "','suspended','test retry','" + user + "','issuer_operator',gen_random_uuid(),'91000000-0000-4000-8000-000000000002','draft',1,'" + batch + "','" + user + "','" + user + "'); " +
			"INSERT INTO bank.card_operations(entity_id,id,card_id,action,reason,actor_role,executor_identity,request_id) VALUES " +
			"('" + entity + "','" + expiryOperation + "','" + card + "','expire','daily expiry','system','test-expiry-worker',gen_random_uuid()); " +
			"INSERT INTO bank.card_expiry_runs(entity_id,id,run_date,executor_identity,item_count) VALUES " +
			"('" + entity + "','" + expiryRun + "',CURRENT_DATE,'test-expiry-worker',1); " +
			"INSERT INTO bank.card_expiry_run_items(entity_id,id,expiry_run_id,card_id,operation_id) VALUES " +
			"('" + entity + "','" + expiryItem + "','" + expiryRun + "','" + card + "','" + expiryOperation + "'); " +
			"UPDATE bank.card_expiry_run_items SET status='processing',lease_owner='worker',lease_expires_at=now()+interval '1 minute',lease_version=1 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='pending',lease_owner=NULL,lease_expires_at=NULL,attempt_count=1,automatic_retry_count=1 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='processing',lease_owner='worker',lease_expires_at=now()+interval '1 minute',lease_version=2 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='pending',lease_owner=NULL,lease_expires_at=NULL,attempt_count=2,automatic_retry_count=2 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='processing',lease_owner='worker',lease_expires_at=now()+interval '1 minute',lease_version=3 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='pending',lease_owner=NULL,lease_expires_at=NULL,attempt_count=3,automatic_retry_count=3 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='processing',lease_owner='worker',lease_expires_at=now()+interval '1 minute',lease_version=4 WHERE id='" + expiryItem + "'; " +
			"UPDATE bank.card_expiry_run_items SET status='manual_retry_required',lease_owner=NULL,lease_expires_at=NULL,attempt_count=4,failure_code='transient' WHERE id='" + expiryItem + "'; COMMIT;"
		h.must(h.shard, setup)
		h.reject(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='"+entity+"'; UPDATE bank.cards SET status='expired' WHERE id='"+card+"'; UPDATE bank.card_expiry_run_items SET status='pending',manual_retry_count=1,manual_retry_by='"+user+"',manual_retry_at=now() WHERE id='"+expiryItem+"'; COMMIT;", "P0001")
		h.must(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='"+entity+"'; UPDATE bank.card_expiry_run_items SET status='pending',manual_retry_count=1,manual_retry_by='"+user+"',manual_retry_at=now() WHERE id='"+expiryItem+"'; UPDATE bank.cards SET status='expired' WHERE id='"+card+"'; COMMIT;")
		h.equal(h.shard, "SELECT status||':'||attempt_count||':'||automatic_retry_count||':'||manual_retry_count FROM bank.card_expiry_run_items WHERE id='"+expiryItem+"';", "pending:4:3:1")
		h.reject(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='"+entity+"'; UPDATE bank.card_expiry_run_items SET status='processing',lease_owner='worker',lease_expires_at=now()+interval '1 minute',lease_version=5 WHERE id='"+expiryItem+"'; COMMIT;", "P0001")
		h.must(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='"+entity+"'; UPDATE bank.card_expiry_run_items SET status='skipped_already_expired' WHERE id='"+expiryItem+"'; UPDATE bank.card_expiry_runs SET skipped_already_expired_count=1,status='completed',completed_at=now() WHERE id='"+expiryRun+"'; COMMIT;")
		h.equal(h.shard, "SELECT status FROM bank.card_expiry_runs WHERE id='"+expiryRun+"';", "completed")
	})
}
