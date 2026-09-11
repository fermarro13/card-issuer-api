package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddress string
	ControlURL  string
	ShardURL    string
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
	control, err := connectionURL("CONTROL", "card_issuer_control", "ci_app_control")
	if err != nil {
		return Config{}, err
	}
	shard, err := connectionURL("SHARD", "card_issuer_shard_01", "ci_app_shard")
	if err != nil {
		return Config{}, err
	}
	return Config{HTTPAddress: address, ControlURL: control, ShardURL: shard}, nil
}
