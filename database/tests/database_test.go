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
	t                            *testing.T
	psql, runner, control, shard string
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
	args := []string{"-NoProfile", "-File", h.runner, "-Action", action, "-Psql", h.psql, "-ControlDatabase", h.control, "-ShardDatabase", h.shard}
	args = append(args, extra...)
	out, err := exec.Command("pwsh", args...).CombinedOutput()
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
	runner, err := filepath.Abs("../Invoke-Database.ps1")
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("ci_test_%d", time.Now().UnixNano())
	h := harness{t, psql, runner, prefix + "_control", prefix + "_shard"}
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
		h.equal(h.shard, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname='bank' AND c.relkind='r' AND c.relrowsecurity AND c.relforcerowsecurity;", "12")
		h.equal(h.control, "SELECT count(*) FROM pg_tables WHERE schemaname='control';", "5")
		h.equal(h.control, "SELECT count(*) FROM pg_roles WHERE rolname IN ('ci_owner','ci_auth_runtime','ci_routing_reader','ci_business_runtime') AND NOT (rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb);", "4")
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
		h.equal(h.control, "SELECT count(*) FROM ci_meta.schema_migrations;", "1")
		h.equal(h.shard, "SELECT count(*) FROM ci_meta.schema_migrations;", "1")
	})
	t.Run("migration_checksums_and_rollback", func(t *testing.T) {
		h := h
		h.t = t
		root := t.TempDir()
		for _, kind := range []string{"control", "shard"} {
			if err := os.Mkdir(filepath.Join(root, kind), 0700); err != nil {
				t.Fatal(err)
			}
			data := read(t, "../migrations/"+kind+"/001_initial.sql")
			if err := os.WriteFile(filepath.Join(root, kind, "001_initial.sql"), []byte(data), 0600); err != nil {
				t.Fatal(err)
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
		h.equal(h.control, "SELECT count(*) FROM ci_meta.schema_migrations;", "1")
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
	})
	t.Run("atomic_database_rollback", func(t *testing.T) {
		h := h
		h.t = t
		h.reject(h.shard, "BEGIN; SET LOCAL ROLE ci_business_runtime; SET LOCAL app.entity_id='10000000-0000-4000-8000-000000000001'; UPDATE bank.cards SET status='active',version=version+1 WHERE id='60000000-0000-4000-8000-000000000001'; UPDATE bank.card_status_batches SET status='succeeded'; UPDATE bank.card_operations SET status='succeeded'; INSERT INTO bank.card_status_batch_items(entity_id,batch_id,card_id,operation_id,previous_status,outcome) VALUES ('10000000-0000-4000-8000-000000000001','80000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001','70000000-0000-4000-8000-000000000001','issued','applied'); UPDATE bank.cards SET status='invalid'; COMMIT;", "23514")
		h.equal(h.shard, "SELECT status||':'||version FROM bank.cards WHERE id='60000000-0000-4000-8000-000000000001';", "issued:1")
		h.equal(h.shard, "SELECT status FROM bank.card_status_batches;", "queued")
		h.equal(h.shard, "SELECT status FROM bank.card_operations;", "queued")
		h.equal(h.shard, "SELECT count(*) FROM bank.card_status_batch_items;", "0")
	})
}
