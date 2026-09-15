package executor

import "testing"

func TestConfigValidation(t *testing.T) {
	environment := map[string]string{
		"EXECUTOR_ID": "executor-a", "EXECUTOR_HTTP_ADDR": ":8090", "EXECUTOR_CONTROL_DATABASE_URL": "postgres://executor:password@localhost/control", "EXECUTOR_SHARD_DATABASE_URL": "postgres://executor:password@localhost/shard", "EXECUTOR_SHARD_ID": "shard-a", "EXECUTOR_POLL_INTERVAL": "1s", "EXECUTOR_MAX_CLAIMS": "2", "EXECUTOR_PER_BANK_CONCURRENCY": "1", "EXECUTOR_LEASE_DURATION": "30s", "EXECUTOR_BATCH_MAX_ATTEMPTS": "3", "EXECUTOR_BATCH_INITIAL_BACKOFF": "1s", "EXECUTOR_BATCH_MAX_BACKOFF": "4s", "EXECUTOR_BATCH_SIZE_CAP": "200", "EXECUTOR_EXPIRY_BACKOFF": "1s", "EXECUTOR_DRAIN_TIMEOUT": "10s",
	}
	getenv := func(name string) string { return environment[name] }
	if _, err := load(getenv); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	delete(environment, "EXECUTOR_ID")
	if _, err := load(getenv); err == nil {
		t.Fatal("missing identity was accepted")
	}
}
