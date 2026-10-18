// Package storage provides PostgreSQL access.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options tune the connection pool.
type Options struct {
	// MaxConns is the pool size. Every in-flight update that touches the
	// database needs a connection, so it bounds the real parallelism; keep the
	// sum over all instances below PostgreSQL's max_connections (100 by default).
	MaxConns int32
}

// Pool settings that do not depend on the deployment.
const (
	connectTimeout = 5 * time.Second
	// statementTimeout stops a runaway query instead of letting it hold a
	// connection forever; every statement of the bot takes milliseconds
	// (the heaviest, /stats, takes a fraction of a second at 200 000 users).
	statementTimeout = 30 * time.Second
	// idleInTxTimeout releases a connection held by a transaction whose process
	// died. It must exceed the longest transaction, a daily draw that
	// re-checks subscriptions (drawTimeout is 5 minutes).
	idleInTxTimeout = 10 * time.Minute
)

// Connect opens a connection pool and verifies it with a ping.
func Connect(ctx context.Context, url string, opts Options) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, opts)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func poolConfig(url string, opts Options) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// pgx puts the offending part of the URL, possibly the password, into
		// its message; do not repeat it.
		return nil, fmt.Errorf("parse DATABASE_URL: invalid connection string")
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	cfg.MinConns = min(2, cfg.MaxConns)
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "giveaway-bot"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = fmt.Sprint(statementTimeout.Milliseconds())
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = fmt.Sprint(idleInTxTimeout.Milliseconds())
	return cfg, nil
}
