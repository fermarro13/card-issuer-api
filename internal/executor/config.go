package executor

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Identity             string
	OperationalAddress   string
	ControlURL           string
	ShardURL             string
	ShardID              string
	PollInterval         time.Duration
	MaxClaims            int
	PerBankConcurrency   int
	LeaseDuration        time.Duration
	BatchMaxAttempts     int
	BatchInitialBackoff  time.Duration
	BatchMaximumBackoff  time.Duration
	BatchSizeCap         int
	ExpiryInitialBackoff time.Duration
	DrainTimeout         time.Duration
}

func FromEnvironment() (Config, error) { return load(os.Getenv) }

func load(getenv func(string) string) (Config, error) {
	required := func(name string) (string, error) {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			return "", errors.New(name + " is required")
		}
		return value, nil
	}
	identity, err := required("EXECUTOR_ID")
	if err != nil || len(identity) > 128 {
		return Config{}, errors.New("invalid EXECUTOR_ID")
	}
	address, err := required("EXECUTOR_HTTP_ADDR")
	if err != nil {
		return Config{}, err
	}
	if _, _, err = net.SplitHostPort(address); err != nil {
		return Config{}, errors.New("invalid EXECUTOR_HTTP_ADDR")
	}
	controlURL, err := required("EXECUTOR_CONTROL_DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	shardURL, err := required("EXECUTOR_SHARD_DATABASE_URL")
	if err != nil {
		return Config{}, err
	}
	for _, value := range []string{controlURL, shardURL} {
		u, parseErr := url.Parse(value)
		if parseErr != nil || u.Scheme != "postgres" || u.Host == "" || u.User == nil {
			return Config{}, errors.New("invalid executor database URL")
		}
	}
	shardID, err := required("EXECUTOR_SHARD_ID")
	if err != nil || len(shardID) > 63 {
		return Config{}, errors.New("invalid EXECUTOR_SHARD_ID")
	}
	duration := func(name string) (time.Duration, error) {
		value, valueErr := required(name)
		if valueErr != nil {
			return 0, valueErr
		}
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil || parsed <= 0 {
			return 0, errors.New("invalid " + name)
		}
		return parsed, nil
	}
	positive := func(name string) (int, error) {
		value, valueErr := required(name)
		if valueErr != nil {
			return 0, valueErr
		}
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 {
			return 0, errors.New("invalid " + name)
		}
		return parsed, nil
	}
	poll, err := duration("EXECUTOR_POLL_INTERVAL")
	if err != nil {
		return Config{}, err
	}
	lease, err := duration("EXECUTOR_LEASE_DURATION")
	if err != nil {
		return Config{}, err
	}
	initial, err := duration("EXECUTOR_BATCH_INITIAL_BACKOFF")
	if err != nil {
		return Config{}, err
	}
	maximum, err := duration("EXECUTOR_BATCH_MAX_BACKOFF")
	if err != nil || maximum < initial {
		return Config{}, errors.New("invalid EXECUTOR_BATCH_MAX_BACKOFF")
	}
	expiryBackoff, err := duration("EXECUTOR_EXPIRY_BACKOFF")
	if err != nil {
		return Config{}, err
	}
	drain, err := duration("EXECUTOR_DRAIN_TIMEOUT")
	if err != nil {
		return Config{}, err
	}
	maxClaims, err := positive("EXECUTOR_MAX_CLAIMS")
	if err != nil {
		return Config{}, err
	}
	perBank, err := positive("EXECUTOR_PER_BANK_CONCURRENCY")
	if err != nil {
		return Config{}, err
	}
	maxAttempts, err := positive("EXECUTOR_BATCH_MAX_ATTEMPTS")
	if err != nil {
		return Config{}, err
	}
	batchSize, err := positive("EXECUTOR_BATCH_SIZE_CAP")
	if err != nil {
		return Config{}, err
	}
	return Config{identity, address, controlURL, shardURL, shardID, poll, maxClaims, perBank, lease, maxAttempts, initial, maximum, batchSize, expiryBackoff, drain}, nil
}

func batchBackoff(initial, maximum time.Duration, attempt int) time.Duration {
	if attempt <= 1 {
		return initial
	}
	value := initial
	for range attempt - 1 {
		if value >= maximum/2 {
			return maximum
		}
		value *= 2
	}
	return value
}
