package database

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, connectionURL string) (*pgxpool.Pool, error) {
	configuration, err := pgxpool.ParseConfig(connectionURL)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	configuration.MaxConns = 5
	configuration.MinConns = 0
	configuration.MaxConnLifetime = 30 * time.Minute
	configuration.MaxConnIdleTime = 5 * time.Minute
	configuration.HealthCheckPeriod = 30 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		return nil, errors.New("database pool initialization failed")
	}
	return pool, nil
}
