package storage

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPoolConfig(t *testing.T) {
	cfg, err := poolConfig("postgres://u:p@localhost:5432/db?sslmode=disable", Options{MaxConns: 40})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConns != 40 || cfg.MinConns != 2 {
		t.Errorf("pool size: min %d max %d", cfg.MinConns, cfg.MaxConns)
	}
	if cfg.MaxConnLifetime <= 0 || cfg.MaxConnIdleTime <= 0 || cfg.HealthCheckPeriod <= 0 {
		t.Error("connections must be recycled and health-checked")
	}
	if cfg.ConnConfig.ConnectTimeout != connectTimeout {
		t.Errorf("connect timeout = %v", cfg.ConnConfig.ConnectTimeout)
	}
	if cfg.ConnConfig.RuntimeParams["statement_timeout"] != "30000" ||
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] != "600000" {
		t.Errorf("runtime params = %v", cfg.ConnConfig.RuntimeParams)
	}
}

func TestPoolConfigSmallPoolAndDefaults(t *testing.T) {
	cfg, err := poolConfig("postgres://u:p@localhost/db", Options{MaxConns: 1})
	if err != nil || cfg.MaxConns != 1 || cfg.MinConns != 1 {
		t.Errorf("tiny pool: %+v, %v", cfg, err)
	}
	cfg, err = poolConfig("postgres://u:p@localhost/db", Options{})
	if err != nil || cfg.MaxConns < 4 {
		t.Errorf("default pool: %d, %v", cfg.MaxConns, err)
	}
}

func TestPoolConfigDoesNotEchoThePassword(t *testing.T) {
	_, err := poolConfig("postgres://user:TopSecret99@local host:bad/db", Options{})
	if err == nil {
		t.Fatal("expected an error for a broken URL")
	}
	if strings.Contains(err.Error(), "TopSecret99") {
		t.Errorf("the password is in the error: %v", err)
	}
}

func TestConnectAppliesTheSettings(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := Connect(ctx, url, Options{MaxConns: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.Config().MaxConns != 7 {
		t.Errorf("MaxConns = %d", pool.Config().MaxConns)
	}
	var timeout, app string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SHOW application_name`).Scan(&app); err != nil {
		t.Fatal(err)
	}
	if timeout != "30s" || app != "giveaway-bot" {
		t.Errorf("statement_timeout = %s, application_name = %s", timeout, app)
	}
}
